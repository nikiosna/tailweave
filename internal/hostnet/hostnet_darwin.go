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
	for _, p := range h.Routes {
		if err := run("", "route", "-q", "-n", "add", "-inet", "-net", p.Masked().String(), "-interface", h.Tun); err != nil {
			return err
		}
	}
	return run("", "ifconfig", "lo0", "alias", h.DNS.String(), "netmask", "255.255.255.255")
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
	for _, p := range h.Routes {
		if h.routedHere(p) {
			_ = run("", "route", "-q", "-n", "delete", "-inet", "-net", p.Masked().String())
		}
	}
	h.domains, h.set = nil, false
}

// routedHere reports whether the route the kernel picks for p leads into the utun.
func (h *Host) routedHere(p netip.Prefix) bool {
	out, err := exec.Command("route", "-n", "get", "-inet", "-net", p.Masked().String()).Output()
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
