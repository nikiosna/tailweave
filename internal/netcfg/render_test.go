package netcfg

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/Nikiosna/tailweave/internal/weave"
)

func ip(s string) netip.Addr    { return netip.MustParseAddr(s) }
func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

var (
	plan = weave.Plan{Routes: map[netip.Addr]weave.Target{
		ip("198.18.0.1"): {Tailnet: "b", IP: ip("100.64.0.1")},
		ip("100.64.0.2"): {Tailnet: "b", IP: ip("100.64.0.2")},
		ip("100.64.0.1"): {Tailnet: "a", IP: ip("100.64.0.1")},
	}, Subnets: map[netip.Prefix]weave.SubnetTarget{
		pfx("10.99.0.0/24"):                     {Tailnet: "a", Prefix: pfx("10.99.0.0/24")},
		pfx("198.18.1.0/24"):                    {Tailnet: "b", Prefix: pfx("10.99.0.0/24")},
		pfx("fd7a:115c:a1e0:b1a:0:8:a63:0/120"): {Tailnet: "b", Prefix: pfx("fd7a:115c:a1e0:b1a:0:8:a63:0/120")},
	}, Local: map[string]weave.Local{
		"a": {IP: ip("100.64.0.3")},
		"b": {IP: ip("100.64.0.3"), PeerAPIPort: 4242},
	}}
	nss = []Namespace{
		{Tailnet: "a", Netns: "tw-a", Gateway: ip("10.200.1.2"), HostIP: ip("10.200.1.1"), Veth: "twn0", Tun: "ts-a"},
		{Tailnet: "b", Netns: "tw-b", Gateway: ip("10.200.2.2"), HostIP: ip("10.200.2.1"), Veth: "twn1", Tun: "ts-b"},
	}
)

func TestHostRoutes(t *testing.T) {
	got, err := HostRoutes(plan, nss, []netip.Prefix{CGNAT, netip.MustParsePrefix("198.18.0.0/15")})
	if err != nil {
		t.Fatal(err)
	}
	want := `route flush table 5180
route replace unreachable 100.64.0.0/10 table 5180
route replace unreachable 198.18.0.0/15 table 5180
route replace 100.64.0.1/32 via 10.200.1.2 table 5180
route replace 100.64.0.2/32 via 10.200.2.2 table 5180
route replace 198.18.0.1/32 via 10.200.2.2 table 5180
route replace 10.99.0.0/24 via 10.200.1.2 table 5180
route replace 198.18.1.0/24 via 10.200.2.2 table 5180
`
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if _, err := HostRoutes(plan, nss[:1], nil); err == nil {
		t.Error("expected error for a tailnet without namespace")
	}
}

func TestNamespaceRules(t *testing.T) {
	want := `table inet tailweave
delete table inet tailweave
table inet tailweave {
	chain prerouting {
		type nat hook prerouting priority dstnat; policy accept;
		ip daddr 198.18.0.1 dnat ip to 100.64.0.1
		ip daddr 198.18.1.0/24 dnat ip prefix to 10.99.0.0/24
		iifname "ts-b" ip daddr 100.64.0.3 tcp dport 4242 accept
		iifname "ts-b" ip daddr 100.64.0.3 meta l4proto { tcp, udp, icmp } dnat ip to 10.200.2.1
	}
	chain forward {
		type filter hook forward priority filter; policy accept;
		iifname "ts-b" ct state established,related accept
		iifname "ts-b" oifname "twn1" ip saddr { 100.64.0.1, 100.64.0.2 } ip daddr 10.200.2.1 ct status dnat accept
		iifname "ts-b" drop
		iifname "twn1" oifname "twn1" drop
	}
	chain postrouting {
		type nat hook postrouting priority srcnat - 1; policy accept;
		oifname "ts-b" ip saddr != 100.64.0.0/10 masquerade
		oifname "twn1" ct status dnat snat ip to ip saddr map { 100.64.0.2 : 100.64.0.2, 100.64.0.1 : 198.18.0.1 }
	}
}
`
	if got := NamespaceRules(plan, nss[1]); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}

	// Tailnet a: no PeerAPI port known, no mapped peers.
	want = `table inet tailweave
delete table inet tailweave
table inet tailweave {
	chain prerouting {
		type nat hook prerouting priority dstnat; policy accept;
		iifname "ts-a" ip daddr 100.64.0.3 meta l4proto { tcp, udp, icmp } dnat ip to 10.200.1.1
	}
	chain forward {
		type filter hook forward priority filter; policy accept;
		iifname "ts-a" ct state established,related accept
		iifname "ts-a" oifname "twn0" ip saddr { 100.64.0.1 } ip daddr 10.200.1.1 ct status dnat accept
		iifname "ts-a" drop
		iifname "twn0" oifname "twn0" drop
	}
	chain postrouting {
		type nat hook postrouting priority srcnat - 1; policy accept;
		oifname "ts-a" ip saddr != 100.64.0.0/10 masquerade
		oifname "twn0" ct status dnat snat ip to ip saddr map { 100.64.0.1 : 100.64.0.1 }
	}
}
`
	if got := NamespaceRules(plan, nss[0]); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestNamespaceRulesWithoutSelf(t *testing.T) {
	// Before the instance knows its IP, or without routed peers, nothing gets in.
	p := weave.Plan{Routes: plan.Routes}
	got := NamespaceRules(p, nss[1])
	if strings.Contains(got, "10.200.2.1") || strings.Contains(got, "map") {
		t.Errorf("inbound rules without a self IP:\n%s", got)
	}
	p = weave.Plan{Local: plan.Local}
	if got := NamespaceRules(p, nss[1]); strings.Contains(got, "10.200.2.1") {
		t.Errorf("inbound rules without routed peers:\n%s", got)
	}
}

func TestHostRules(t *testing.T) {
	want := `table ip tailweave
delete table ip tailweave
table ip tailweave {
	chain postrouting {
		type nat hook postrouting priority srcnat; policy accept;
		ip saddr 10.201.0.0/16 oifname != "twh*" masquerade
	}
	chain forward {
		type filter hook forward priority filter; policy accept;
		oifname "twh*" ct state established,related accept
		oifname "twh*" drop
	}
}
`
	if got := HostRules(netip.MustParsePrefix("10.201.0.0/16")); got != want {
		t.Errorf("HostRules:\n%s\nwant:\n%s", got, want)
	}
}

func TestExitRoutes(t *testing.T) {
	blocked := "route flush table 5181\nroute replace unreachable default table 5181\n"
	p := plan
	v4, v6 := ExitRoutes(p, nss)
	if v4 != blocked || v6 != blocked {
		t.Errorf("without exit node: %q, %q", v4, v6)
	}
	p.Exit = &weave.Exit{Tailnet: "b", PeerID: "4", Name: "exit-b"}
	v4, v6 = ExitRoutes(p, nss)
	if want := "route flush table 5181\nroute replace default via 10.200.2.2 table 5181\n"; v4 != want || v6 != blocked {
		t.Errorf("exit node in b: %q, %q", v4, v6)
	}
	// Its tailnet is not running: blocked, not direct.
	if v4, _ = ExitRoutes(p, nss[:1]); v4 != blocked {
		t.Errorf("exit tailnet without namespace: %q", v4)
	}
}
