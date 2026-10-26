// Package hostnet configures the host for the userspace router: the TUN device's
// address, routes for the tailnet ranges into it, the DNS service address and the
// registration of the tailnet domains with the system resolver. It needs root.
//
// Only locally generated traffic is routed into the TUN; the router additionally drops
// everything that does not come from the TUN's own address, so neither the LAN nor
// containers on the host get into a tailnet.
package hostnet

import (
	"bytes"
	"fmt"
	"net/netip"
	"os/exec"
	"slices"
	"strings"
)

// Host is the host side of the router's TUN device.
type Host struct {
	Tun     string         // name of the TUN device, as the OS reports it
	HostIP  netip.Addr     // the TUN's address; the source of all host traffic into it
	HostIP6 netip.Addr     // the same for IPv6; zero: no IPv6
	DNS     netip.Addr     // address of the DNS service on this machine
	Routes  []netip.Prefix // ranges always routed into the TUN

	extra   []netip.Prefix // routes added by SetRoutes
	exit    bool           // internet traffic goes into the TUN (SetExit)
	domains []string
	set     bool // domains were registered at least once
}

// SetRoutes routes ps into the TUN in addition to Routes, and no longer what an earlier
// call routed that is not in ps.
func (h *Host) SetRoutes(ps []netip.Prefix) error {
	add, del := diffRoutes(h.extra, ps)
	for _, p := range del {
		h.delRoute(p)
		h.extra = slices.DeleteFunc(h.extra, func(x netip.Prefix) bool { return x == p })
	}
	for _, p := range add {
		if err := h.addRoute(p); err != nil {
			return err
		}
		h.extra = append(h.extra, p)
	}
	return nil
}

// SetExit sends this machine's internet traffic into the TUN, for an exit node, or no
// longer. The networks the host reaches on its own stay where they are. IPv6 internet
// traffic is blocked meanwhile; exit nodes carry IPv4 only so far.
func (h *Host) SetExit(on bool) error {
	if on == h.exit {
		return nil
	}
	if !on {
		h.unsetExit()
		h.exit = false
		return nil
	}
	if err := h.setExit(); err != nil {
		h.unsetExit()
		return err
	}
	h.exit = true
	return nil
}

// diffRoutes returns the prefixes in want but not in have, and those in have but not in
// want.
func diffRoutes(have, want []netip.Prefix) (add, del []netip.Prefix) {
	for _, p := range want {
		if !slices.Contains(have, p) && !slices.Contains(add, p) {
			add = append(add, p)
		}
	}
	for _, p := range have {
		if !slices.Contains(want, p) {
			del = append(del, p)
		}
	}
	return add, del
}

// srcFor returns the TUN's address in the family of p.
func (h *Host) srcFor(p netip.Prefix) netip.Addr {
	if p.Addr().Is4() {
		return h.HostIP
	}
	return h.HostIP6
}

// SetDomains makes the system resolver ask the DNS service for names in domains. It only
// does something when domains changed.
func (h *Host) SetDomains(domains []string) error {
	if h.set && slices.Equal(domains, h.domains) {
		return nil
	}
	if err := h.setDomains(domains); err != nil {
		return err
	}
	h.domains, h.set = slices.Clone(domains), true
	return nil
}

func run(stdin, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return nil
}
