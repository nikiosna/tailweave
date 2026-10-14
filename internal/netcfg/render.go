// Package netcfg turns a weave.Plan into Linux network configuration: a routing table
// on the host that sends every routed IP into its tailnet's namespace, per-namespace
// nftables rules that translate mapped IPs and masquerade host traffic, and a host
// nftables table that lets the namespaces reach the outside world.
//
// Only the host itself reaches the tailnets. The routing table applies to locally
// generated traffic only, the host forwards nothing new into a namespace (so neither the
// LAN nor another namespace gets in), and a namespace forwards nothing new that comes
// from its tailnet (so peers cannot use it as a router).
package netcfg

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/Nikiosna/tailweave/internal/weave"
)

// Table is the host routing table tailweave owns; RulePref is the priority of the ip
// rule that consults it for locally generated traffic. Both are below Tailscale's own
// table 52 / pref 5270.
const (
	Table    = 5250
	RulePref = 5250
	nftTable = "tailweave"
)

// VethPrefix starts the host side names of the namespaces' veth pairs ("twh0", ...).
const VethPrefix = "twh"

// CGNAT is the range Tailscale assigns node addresses from.
var CGNAT = netip.MustParsePrefix("100.64.0.0/10")

// Namespace is where a tailnet's tailscaled runs.
type Namespace struct {
	Tailnet string
	Netns   string     // network namespace name
	Gateway netip.Addr // the namespace's address on its veth, as seen from the host
	Tun     string     // tailscaled's TUN interface inside the namespace
}

// HostRoutes renders `ip -batch` input that replaces the contents of Table. Prefixes in
// blocked are unreachable unless a more specific route exists, so unrouted tailnet IPs do
// not leak to the default route.
func HostRoutes(p weave.Plan, nss []Namespace, blocked []netip.Prefix) (string, error) {
	gw := map[string]netip.Addr{}
	for _, ns := range nss {
		gw[ns.Tailnet] = ns.Gateway
	}
	var b strings.Builder
	fmt.Fprintf(&b, "route flush table %d\n", Table)
	for _, pfx := range blocked {
		fmt.Fprintf(&b, "route replace unreachable %s table %d\n", pfx.Masked(), Table)
	}
	for _, ip := range sortedIPs(p.Routes) {
		t := p.Routes[ip]
		g, ok := gw[t.Tailnet]
		if !ok {
			return "", fmt.Errorf("no namespace for tailnet %q", t.Tailnet)
		}
		fmt.Fprintf(&b, "route replace %s/32 via %s table %d\n", ip, g, Table)
	}
	return b.String(), nil
}

// NamespaceRules renders an `nft -f` script that atomically replaces tailweave's table in
// the namespace of ns.
func NamespaceRules(p weave.Plan, ns Namespace) string {
	var b strings.Builder
	// Declaring the table first makes the delete succeed when it does not exist yet.
	fmt.Fprintf(&b, "table inet %s\ndelete table inet %s\n", nftTable, nftTable)
	fmt.Fprintf(&b, "table inet %s {\n", nftTable)
	b.WriteString("\tchain prerouting {\n\t\ttype nat hook prerouting priority dstnat; policy accept;\n")
	for _, ip := range sortedIPs(p.Routes) {
		if t := p.Routes[ip]; t.Tailnet == ns.Tailnet && t.IP != ip {
			fmt.Fprintf(&b, "\t\tip daddr %s dnat ip to %s\n", ip, t.IP)
		}
	}
	b.WriteString("\t}\n")
	// Peers only get answers to the host's connections forwarded, nothing of their own.
	b.WriteString("\tchain forward {\n\t\ttype filter hook forward priority filter; policy accept;\n")
	fmt.Fprintf(&b, "\t\tiifname %q ct state established,related accept\n", ns.Tun)
	fmt.Fprintf(&b, "\t\tiifname %q drop\n", ns.Tun)
	b.WriteString("\t}\n")
	b.WriteString("\tchain postrouting {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
	// Traffic from the host leaves with the instance's own tailnet IP; tailscaled's own
	// traffic already has one.
	fmt.Fprintf(&b, "\t\toifname %q ip saddr != %s masquerade\n", ns.Tun, CGNAT)
	b.WriteString("\t}\n}\n")
	return b.String()
}

// HostRules renders an `nft -f` script that atomically replaces tailweave's table on the
// host: traffic from the namespaces' transit addresses leaves the host masqueraded, so
// tailscaled can reach its control server and DERP, and nothing but answers is forwarded
// into a namespace, neither from the LAN nor from another namespace.
func HostRules(transit netip.Prefix) string {
	var b strings.Builder
	fmt.Fprintf(&b, "table ip %s\ndelete table ip %s\n", nftTable, nftTable)
	fmt.Fprintf(&b, "table ip %s {\n", nftTable)
	b.WriteString("\tchain postrouting {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
	fmt.Fprintf(&b, "\t\tip saddr %s oifname != \"%s*\" masquerade\n", transit.Masked(), VethPrefix)
	b.WriteString("\t}\n")
	b.WriteString("\tchain forward {\n\t\ttype filter hook forward priority filter; policy accept;\n")
	fmt.Fprintf(&b, "\t\toifname \"%s*\" ct state established,related accept\n", VethPrefix)
	fmt.Fprintf(&b, "\t\toifname \"%s*\" drop\n", VethPrefix)
	b.WriteString("\t}\n}\n")
	return b.String()
}

func sortedIPs(m map[netip.Addr]weave.Target) []netip.Addr {
	ips := make([]netip.Addr, 0, len(m))
	for ip := range m {
		ips = append(ips, ip)
	}
	slices.SortFunc(ips, netip.Addr.Compare)
	return ips
}
