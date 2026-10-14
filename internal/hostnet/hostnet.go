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
	Tun    string         // name of the TUN device, as the OS reports it
	HostIP netip.Addr     // the TUN's address; the source of all host traffic into it
	DNS    netip.Addr     // address of the DNS service on this machine
	Routes []netip.Prefix // ranges routed into the TUN

	domains []string
	set     bool // domains were registered at least once
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
