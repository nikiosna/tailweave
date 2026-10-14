package hostnet

import "strings"

// TunName is the name tailweave asks for when it creates its TUN device; macOS picks the
// next free utun.
const TunName = "utun"

// dnsKey is where tailweave publishes its DNS settings in the dynamic store. configd
// merges them into the system's resolver configuration like a VPN's.
const dnsKey = "State:/Network/Service/tailweave/DNS"

// Setup gives the utun its address, routes the tailnet ranges into it and puts the DNS
// service address on lo0.
func (h *Host) Setup() error {
	h.Teardown()
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

// Teardown removes the DNS settings, the lo0 alias and the routes; the utun's own
// address goes away with the device. Errors are ignored: parts may not exist.
func (h *Host) Teardown() {
	_ = run("remove "+dnsKey+"\n", "scutil")
	if h.DNS.IsValid() {
		_ = run("", "ifconfig", "lo0", "-alias", h.DNS.String())
	}
	for _, p := range h.Routes {
		_ = run("", "route", "-q", "-n", "delete", "-inet", "-net", p.Masked().String())
	}
	h.domains, h.set = nil, false
}
