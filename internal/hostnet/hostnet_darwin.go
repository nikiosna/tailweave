package hostnet

import (
	"net/netip"
	"os/exec"
	"strings"
)

// TunName is the name tailweave asks for when it creates its TUN device; macOS picks the
// next free utun.
const TunName = "utun"

// dnsKey is where tailweave publishes its DNS settings in the dynamic store. configd
// merges them into the system's resolver configuration like a VPN's.
const dnsKey = "State:/Network/Service/tailweave/DNS"

// Cleanup removes what an earlier run left behind: the DNS settings and the lo0 alias.
// Its routes went away with its utun.
func Cleanup(dns netip.Addr) {
	_ = run("remove "+dnsKey+"\n", "scutil")
	if dns.IsValid() {
		_ = run("", "ifconfig", "lo0", "-alias", dns.String())
	}
}

// Setup gives the utun its address, routes the tailnet ranges into it and puts the DNS
// service address on lo0.
func (h *Host) Setup() error {
	ip := h.HostIP.String()
	if err := run("", "ifconfig", h.Tun, "inet", ip, ip, "netmask", "255.255.255.255", "up"); err != nil {
		return err
	}
	if h.HostIP6.IsValid() {
		if err := run("", "ifconfig", h.Tun, "inet6", h.HostIP6.String(), "prefixlen", "128"); err != nil {
			return err
		}
	}
	for _, p := range h.Routes {
		if err := h.addRoute(p); err != nil {
			return err
		}
	}
	return run("", "ifconfig", "lo0", "alias", h.DNS.String(), "netmask", "255.255.255.255")
}

func (h *Host) addRoute(p netip.Prefix) error {
	if !h.srcFor(p).IsValid() {
		return nil // no IPv6 on the utun
	}
	return run("", "route", "-q", "-n", "add", family(p), "-net", p.Masked().String(), "-interface", h.Tun)
}

// delRoute deletes the route for p if it leads into the utun.
func (h *Host) delRoute(p netip.Prefix) {
	if h.routedHere(p) {
		_ = run("", "route", "-q", "-n", "delete", family(p), "-net", p.Masked().String())
	}
}

// exitRoutes are the halves of both address spaces: more specific than the default route,
// which stays, so Tailscale's own sockets still bind to the interface it leads to, and less
// specific than any network the host reaches on its own.
var exitRoutes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1"),
	netip.MustParsePrefix("::/1"), netip.MustParsePrefix("8000::/1"),
}

func (h *Host) setExit() error {
	for _, p := range exitRoutes {
		if err := h.addRoute(p); err != nil {
			return err
		}
	}
	return nil
}

func (h *Host) unsetExit() {
	for _, p := range exitRoutes {
		h.delRoute(p)
	}
}

func family(p netip.Prefix) string {
	if p.Addr().Is4() {
		return "-inet"
	}
	return "-inet6"
}

// setDomains registers the DNS service for domains: as resolver for names below them and
// as search domains, so short host names work too.
func (h *Host) setDomains(domains []string) error {
	if len(domains) == 0 {
		return run("remove "+dnsKey+"\n", "scutil")
	}
	var b strings.Builder
	b.WriteString("d.init\n")
	b.WriteString("d.add ServerAddresses * " + h.DNS.String() + "\n")
	b.WriteString("d.add SupplementalMatchDomains * " + strings.Join(domains, " ") + "\n")
	b.WriteString("d.add SearchDomains * " + strings.Join(domains, " ") + "\n")
	b.WriteString("set " + dnsKey + "\n")
	return run(b.String(), "scutil")
}

// Teardown removes the DNS settings, the lo0 alias and the routes into the utun; the
// utun's own address goes away with the device. Routes of other programs stay. Errors are
// ignored: parts may not exist.
func (h *Host) Teardown() {
	Cleanup(h.DNS)
	for _, p := range append(h.Routes, h.extra...) {
		h.delRoute(p)
	}
	if h.exit {
		h.unsetExit()
	}
	h.extra, h.domains, h.set, h.exit = nil, nil, false, false
}

// routedHere reports whether the route the kernel picks for p leads into the utun.
func (h *Host) routedHere(p netip.Prefix) bool {
	out, err := exec.Command("route", "-n", "get", family(p), "-net", p.Masked().String()).Output()
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(out), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(l), "interface:"); ok {
			return strings.TrimSpace(v) == h.Tun
		}
	}
	return false
}
