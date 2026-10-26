// Package netcfg turns a weave.Plan into Linux network configuration: a routing table
// on the host that sends every routed IP into its tailnet's namespace, per-namespace
// nftables rules that translate mapped IPs and masquerade host traffic, and a host
// nftables table that lets the namespaces reach the outside world.
//
// Only the host itself reaches the tailnets. The routing table applies to locally
// generated traffic only, the host forwards nothing new into a namespace (so neither the
// LAN nor another namespace gets in), and a namespace forwards nothing new that comes
// from its tailnet (so peers cannot use it as a router), except connections to the
// instance's own tailnet IP: those are translated to the host's veth address and so end
// on the host itself, from the peer's routed IP.
package netcfg

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"

	"github.com/Nikiosna/tailweave/internal/weave"
)

// Table is the host routing table tailweave owns; RulePref is the priority of the ip
// rule that consults it for locally generated traffic. tailscaled uses table 52 and
// prefs 5210-5270; RulePref sits just before them, at a priority no known VPN client uses.
const (
	Table    = 5180
	RulePref = 5180
	nftTable = "tailweave"
)

// ExitTable holds the default route into the tailnet of the exit node in use. While one is
// picked, three more rules (ExitRules) come after RulePref: Tailscale's own sockets, which
// carry BypassMark, and the networks of the main table stay as they are, everything else
// from this machine goes to ExitTable.
const (
	ExitTable  = 5181
	BypassMark = "0x80000/0xff0000"
)

// ExitRules are the arguments of the ip rules for an exit node, in order.
var ExitRules = [][]string{
	{"pref", fmt.Sprint(RulePref + 1), "fwmark", BypassMark, "lookup", "main"},
	{"pref", fmt.Sprint(RulePref + 2), "iif", "lo", "lookup", "main", "suppress_prefixlength", "0"},
	{"pref", fmt.Sprint(RulePref + 3), "iif", "lo", "lookup", fmt.Sprint(ExitTable)},
}

// VethPrefix starts the host side names of the namespaces' veth pairs ("twh0", ...).
const VethPrefix = "twh"

// CGNAT is the range Tailscale assigns node addresses from; TailscaleULA is its IPv6
// counterpart.
var (
	CGNAT        = netip.MustParsePrefix("100.64.0.0/10")
	TailscaleULA = netip.MustParsePrefix("fd7a:115c:a1e0::/48")
)

// Namespace is where a tailnet's tailscaled runs.
type Namespace struct {
	Tailnet string
	Netns   string     // network namespace name
	Gateway netip.Addr // the namespace's address on its veth, as seen from the host
	HostIP  netip.Addr // the host's address on the veth pair
	Veth    string     // the namespace's end of the veth pair
	Tun     string     // tailscaled's TUN interface inside the namespace

	Gateway6, HostIP6 netip.Addr // the same for IPv6, which carries 4via6 routes; may be zero
}

// HostRoutes renders `ip -batch` input that replaces the IPv4 contents of Table. IPv4
// prefixes in blocked are unreachable unless a more specific route exists, so unrouted
// tailnet IPs do not leak to the default route.
func HostRoutes(p weave.Plan, nss []Namespace, blocked []netip.Prefix) (string, error) {
	gw := map[string]netip.Addr{}
	for _, ns := range nss {
		gw[ns.Tailnet] = ns.Gateway
	}
	var b strings.Builder
	fmt.Fprintf(&b, "route flush table %d\n", Table)
	for _, pfx := range blocked {
		if pfx.Addr().Is4() {
			fmt.Fprintf(&b, "route replace unreachable %s table %d\n", pfx.Masked(), Table)
		}
	}
	for _, ip := range sortedIPs(p.Routes) {
		t := p.Routes[ip]
		g, ok := gw[t.Tailnet]
		if !ok {
			return "", fmt.Errorf("no namespace for tailnet %q", t.Tailnet)
		}
		fmt.Fprintf(&b, "route replace %s/32 via %s table %d\n", ip, g, Table)
	}
	for _, s := range sortedPrefixes(p.Subnets) {
		t := p.Subnets[s]
		if !s.Addr().Is4() {
			continue
		}
		g, ok := gw[t.Tailnet]
		if !ok {
			return "", fmt.Errorf("no namespace for tailnet %q", t.Tailnet)
		}
		fmt.Fprintf(&b, "route replace %s via %s table %d\n", s, g, Table)
	}
	return b.String(), nil
}

// HostRoutes6 renders `ip -6 -batch` input that replaces the IPv6 contents of Table: the
// 4via6 routes, into namespaces with an IPv6 gateway.
func HostRoutes6(p weave.Plan, nss []Namespace, blocked []netip.Prefix) string {
	gw := map[string]netip.Addr{}
	for _, ns := range nss {
		gw[ns.Tailnet] = ns.Gateway6
	}
	var b strings.Builder
	fmt.Fprintf(&b, "route flush table %d\n", Table)
	for _, pfx := range blocked {
		if pfx.Addr().Is6() {
			fmt.Fprintf(&b, "route replace unreachable %s table %d\n", pfx.Masked(), Table)
		}
	}
	for _, s := range sortedPrefixes(p.Subnets) {
		if g := gw[p.Subnets[s].Tailnet]; s.Addr().Is6() && g.IsValid() {
			fmt.Fprintf(&b, "route replace %s via %s table %d\n", s, g, Table)
		}
	}
	return b.String()
}

// ExitRoutes renders `ip -batch` and `ip -6 -batch` input that replaces the contents of
// ExitTable: a default route into the namespace of the exit node's tailnet, or none, which
// blocks the traffic while the tailnet is not running. IPv6 is always blocked; exit nodes
// carry IPv4 only so far.
func ExitRoutes(p weave.Plan, nss []Namespace) (v4, v6 string) {
	v4 = fmt.Sprintf("route flush table %d\n", ExitTable)
	v6 = v4 + fmt.Sprintf("route replace unreachable default table %d\n", ExitTable)
	i := -1
	if p.Exit != nil {
		i = slices.IndexFunc(nss, func(ns Namespace) bool { return ns.Tailnet == p.Exit.Tailnet })
	}
	if i >= 0 {
		v4 += fmt.Sprintf("route replace default via %s table %d\n", nss[i].Gateway, ExitTable)
	} else {
		v4 += fmt.Sprintf("route replace unreachable default table %d\n", ExitTable)
	}
	return v4, v6
}

// NamespaceRules renders an `nft -f` script that atomically replaces tailweave's table in
// the namespace of ns.
//
// Connections from peers to the instance's own tailnet IP go to the host (inbound),
// except tailscaled's PeerAPI. They arrive there from the peer's routed IP; peers that are
// not routed on this machine get nothing forwarded.
func NamespaceRules(p weave.Plan, ns Namespace) string {
	self := p.Local[ns.Tailnet]
	var routed []netip.Addr // real IPs of this tailnet's routed peers
	var b strings.Builder
	// Declaring the table first makes the delete succeed when it does not exist yet.
	fmt.Fprintf(&b, "table inet %s\ndelete table inet %s\n", nftTable, nftTable)
	fmt.Fprintf(&b, "table inet %s {\n", nftTable)
	b.WriteString("\tchain prerouting {\n\t\ttype nat hook prerouting priority dstnat; policy accept;\n")
	for _, ip := range sortedIPs(p.Routes) {
		t := p.Routes[ip]
		if t.Tailnet != ns.Tailnet {
			continue
		}
		routed = append(routed, t.IP)
		if t.IP != ip {
			fmt.Fprintf(&b, "\t\tip daddr %s dnat ip to %s\n", ip, t.IP)
		}
	}
	for _, s := range sortedPrefixes(p.Subnets) {
		if t := p.Subnets[s]; t.Tailnet == ns.Tailnet && t.Prefix != s && s.Addr().Is4() {
			fmt.Fprintf(&b, "\t\tip daddr %s dnat ip prefix to %s\n", s, t.Prefix)
		}
	}
	slices.SortFunc(routed, netip.Addr.Compare)
	inbound := self.IP.IsValid() && len(routed) > 0
	if inbound {
		if self.PeerAPIPort != 0 {
			fmt.Fprintf(&b, "\t\tiifname %q ip daddr %s tcp dport %d accept\n", ns.Tun, self.IP, self.PeerAPIPort)
		}
		fmt.Fprintf(&b, "\t\tiifname %q ip daddr %s meta l4proto { tcp, udp, icmp } dnat ip to %s\n",
			ns.Tun, self.IP, ns.HostIP)
	}
	b.WriteString("\t}\n")
	// Peers get answers to the host's connections forwarded and may connect to the host,
	// nothing else.
	b.WriteString("\tchain forward {\n\t\ttype filter hook forward priority filter; policy accept;\n")
	fmt.Fprintf(&b, "\t\tiifname %q ct state established,related accept\n", ns.Tun)
	if inbound {
		fmt.Fprintf(&b, "\t\tiifname %q oifname %q ip saddr { %s } ip daddr %s ct status dnat accept\n",
			ns.Tun, ns.Veth, joinIPs(routed), ns.HostIP)
	}
	fmt.Fprintf(&b, "\t\tiifname %q drop\n", ns.Tun)
	// What the host sends goes into the tailnet, never back out: with tailscaled down, the
	// host's internet traffic for an exit node would otherwise return and leave directly.
	fmt.Fprintf(&b, "\t\tiifname %q oifname %q drop\n", ns.Veth, ns.Veth)
	b.WriteString("\t}\n")
	// Before tailscaled's own rules (at srcnat), which would masquerade what it forwards.
	b.WriteString("\tchain postrouting {\n\t\ttype nat hook postrouting priority srcnat - 1; policy accept;\n")
	// Traffic from the host leaves with the instance's own tailnet IP; tailscaled's own
	// traffic already has one.
	fmt.Fprintf(&b, "\t\toifname %q ip saddr != %s masquerade\n", ns.Tun, CGNAT)
	if ns.HostIP6.IsValid() {
		fmt.Fprintf(&b, "\t\toifname %q ip6 saddr != %s masquerade\n", ns.Tun, TailscaleULA)
	}
	// Peers reach the host from the IP they are routed at there.
	if inbound {
		var m []string
		for _, ip := range sortedIPs(p.Routes) {
			if t := p.Routes[ip]; t.Tailnet == ns.Tailnet {
				m = append(m, t.IP.String()+" : "+ip.String())
			}
		}
		fmt.Fprintf(&b, "\t\toifname %q ct status dnat snat ip to ip saddr map { %s }\n", ns.Veth, strings.Join(m, ", "))
	}
	b.WriteString("\t}\n}\n")
	return b.String()
}

func joinIPs(ips []netip.Addr) string {
	ss := make([]string, len(ips))
	for i, ip := range ips {
		ss[i] = ip.String()
	}
	return strings.Join(ss, ", ")
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

func sortedPrefixes(m map[netip.Prefix]weave.SubnetTarget) []netip.Prefix {
	ps := make([]netip.Prefix, 0, len(m))
	for p := range m {
		ps = append(ps, p)
	}
	slices.SortFunc(ps, func(a, b netip.Prefix) int {
		if c := a.Addr().Compare(b.Addr()); c != 0 {
			return c
		}
		return a.Bits() - b.Bits()
	})
	return ps
}

func sortedIPs(m map[netip.Addr]weave.Target) []netip.Addr {
	ips := make([]netip.Addr, 0, len(m))
	for ip := range m {
		ips = append(ips, ip)
	}
	slices.SortFunc(ips, netip.Addr.Compare)
	return ips
}
