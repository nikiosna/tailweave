package weave

import (
	"net/netip"
	"slices"
	"testing"
)

var pool = netip.MustParsePrefix("198.18.0.0/15")

func ip(s string) netip.Addr { return netip.MustParseAddr(s) }

func peer(id, name, domain, addr string) Peer {
	return Peer{ID: id, Name: name, DNSName: name + "." + domain + ".", IPs: []netip.Addr{ip(addr), ip("fd7a:115c:a1e0::1")}}
}

// lab mirrors the base lab: both tailnets hand out the same addresses.
func lab() []Tailnet {
	return []Tailnet{
		{Name: "a", Domain: "a.lab", Self: peer("9", "dut-a", "a.lab", "100.64.0.3"), Peers: []Peer{
			peer("1", "peer-a1", "a.lab", "100.64.0.1"),
			peer("2", "peer-a2", "a.lab", "100.64.0.2"),
		}},
		{Name: "b", Domain: "b.lab", Self: peer("9", "dut-b", "b.lab", "100.64.0.4"), Peers: []Peer{
			peer("1", "peer-b1", "b.lab", "100.64.0.1"),
			peer("2", "peer-b2", "b.lab", "100.64.0.2"),
			peer("3", "peer-b3", "b.lab", "100.64.0.3"), // same as dut-a's self IP
		}},
	}
}

func device(t *testing.T, p Plan, tailnet, name string) Device {
	t.Helper()
	for _, d := range p.Devices {
		if d.Tailnet == tailnet && d.Name == name {
			return d
		}
	}
	t.Fatalf("device %s/%s not in plan", tailnet, name)
	return Device{}
}

func conflicts(p Plan, kind string) []Conflict {
	var cs []Conflict
	for _, c := range p.Conflicts {
		if c.Kind == kind {
			cs = append(cs, c)
		}
	}
	return cs
}

func TestUniquePeerRouted(t *testing.T) {
	p := Compute(lab(), nil, Options{AutoMap: true, Pool: pool})
	// Self IPs are never routed and do not collide with peers.
	if got := p.Routes[ip("100.64.0.3")]; got != (Target{"b", ip("100.64.0.3")}) {
		t.Errorf("peer-b3 route = %v", got)
	}
	if got := p.Names["peer-b3.b.lab."]; got != ip("100.64.0.3") {
		t.Errorf("peer-b3 name = %v", got)
	}
	if _, ok := p.Routes[ip("100.64.0.4")]; ok {
		t.Error("self IP of b is routed")
	}
	if !slices.Equal(p.Domains, []string{"a.lab", "b.lab"}) {
		t.Errorf("domains = %v", p.Domains)
	}
}

func TestCollisionAutoMap(t *testing.T) {
	p := Compute(lab(), nil, Options{AutoMap: true, Pool: pool})

	if got := p.Routes[ip("100.64.0.1")]; got != (Target{"a", ip("100.64.0.1")}) {
		t.Errorf("100.64.0.1 -> %v, want tailnet a", got)
	}
	b1 := device(t, p, "b", "peer-b1")
	if b1.RoutedIP != ip("198.18.0.1") || b1.Mapping == nil || !b1.Mapping.Auto {
		t.Errorf("peer-b1 = %+v, want auto-mapped to 198.18.0.1", b1)
	}
	if got := p.Routes[ip("198.18.0.1")]; got != (Target{"b", ip("100.64.0.1")}) {
		t.Errorf("198.18.0.1 -> %v", got)
	}
	if got := p.Names["peer-b1.b.lab."]; got != ip("198.18.0.1") {
		t.Errorf("peer-b1.b.lab -> %v, want mapped IP", got)
	}
	if got := p.Names["peer-a1.a.lab."]; got != ip("100.64.0.1") {
		t.Errorf("peer-a1.a.lab -> %v", got)
	}
	cs := conflicts(p, ConflictIP)
	if len(cs) != 2 || !cs[0].Resolved || cs[0].Key != "100.64.0.1" {
		t.Errorf("ip conflicts = %+v", cs)
	}
	if len(p.Mappings) != 2 {
		t.Errorf("mappings = %+v, want two auto-mappings", p.Mappings)
	}
}

func TestCollisionSkipped(t *testing.T) {
	p := Compute(lab(), nil, Options{AutoMap: false, Pool: pool})
	for _, a := range []string{"100.64.0.1", "100.64.0.2"} {
		if _, ok := p.Routes[ip(a)]; ok {
			t.Errorf("colliding %s is routed", a)
		}
	}
	if _, ok := p.Names["peer-a1.a.lab."]; ok {
		t.Error("name of unrouted device resolves")
	}
	if d := device(t, p, "b", "peer-b1"); d.RoutedIP.IsValid() {
		t.Errorf("peer-b1 routed to %v", d.RoutedIP)
	}
	cs := conflicts(p, ConflictIP)
	if len(cs) != 2 || cs[0].Resolved || len(cs[0].Devices) != 2 {
		t.Errorf("ip conflicts = %+v", cs)
	}
	if len(p.Mappings) != 0 {
		t.Errorf("mappings = %+v", p.Mappings)
	}
}

func TestManualMappingResolves(t *testing.T) {
	m := []Mapping{{Tailnet: "b", PeerID: "1", IP: ip("10.77.0.1")}}
	p := Compute(lab(), m, Options{AutoMap: false, Pool: pool})
	if got := p.Routes[ip("100.64.0.1")]; got != (Target{"a", ip("100.64.0.1")}) {
		t.Errorf("100.64.0.1 -> %v, want tailnet a once b is mapped away", got)
	}
	if got := p.Routes[ip("10.77.0.1")]; got != (Target{"b", ip("100.64.0.1")}) {
		t.Errorf("10.77.0.1 -> %v", got)
	}
	if got := p.Names["peer-b1.b.lab."]; got != ip("10.77.0.1") {
		t.Errorf("peer-b1.b.lab -> %v", got)
	}
	if cs := conflicts(p, ConflictIP); len(cs) != 1 || cs[0].Key != "100.64.0.2" {
		t.Errorf("ip conflicts = %+v, want only 100.64.0.2", cs)
	}
}

func TestManualMappingOverridesAuto(t *testing.T) {
	auto := Compute(lab(), nil, Options{AutoMap: true, Pool: pool}).Mappings
	m := append(slices.Clone(auto), Mapping{Tailnet: "b", PeerID: "1", IP: ip("10.77.0.1")})
	// Drop the auto-mapping of peer-b1 like the API does when the user maps it.
	m = slices.DeleteFunc(m, func(x Mapping) bool { return x.Auto && x.PeerID == "1" })
	p := Compute(lab(), m, Options{AutoMap: true, Pool: pool})
	if d := device(t, p, "b", "peer-b1"); d.RoutedIP != ip("10.77.0.1") || d.Mapping.Auto {
		t.Errorf("peer-b1 = %+v", d)
	}
	if d := device(t, p, "b", "peer-b2"); d.RoutedIP != ip("198.18.0.2") {
		t.Errorf("peer-b2 = %+v, want its old auto-mapping", d)
	}
}

func TestMappingOntoUsedIPRejected(t *testing.T) {
	for _, tc := range []struct{ name, ip string }{
		{"real peer IP", "100.64.0.2"},
		{"self IP", "100.64.0.4"},
		{"other mapping", "10.77.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := []Mapping{
				{Tailnet: "a", PeerID: "1", IP: ip("10.77.0.1")},
				{Tailnet: "b", PeerID: "1", IP: ip(tc.ip)},
			}
			p := Compute(lab(), m, Options{AutoMap: false, Pool: pool})
			cs := conflicts(p, ConflictMapping)
			if len(cs) != 1 || cs[0].Devices[0].Name != "peer-b1" {
				t.Fatalf("mapping conflicts = %+v", cs)
			}
			if d := device(t, p, "b", "peer-b1"); d.Mapping != nil {
				t.Errorf("rejected mapping is active: %+v", d)
			}
			if len(p.Mappings) != 2 {
				t.Errorf("rejected manual mapping was dropped from storage")
			}
		})
	}
}

func TestAutoMappingStable(t *testing.T) {
	opt := Options{AutoMap: true, Pool: pool}
	p1 := Compute(lab(), nil, opt)

	// peer-b1 changes order relative to new peers; existing auto-mappings must not move.
	tn := lab()
	tn[0].Peers = append(tn[0].Peers, peer("5", "peer-a0", "a.lab", "100.64.0.9"))
	tn[1].Peers = append([]Peer{peer("5", "peer-b0", "b.lab", "100.64.0.9")}, tn[1].Peers...)
	p2 := Compute(tn, p1.Mappings, opt)
	for _, name := range []string{"peer-b1", "peer-b2"} {
		if a, b := device(t, p1, "b", name).RoutedIP, device(t, p2, "b", name).RoutedIP; a != b {
			t.Errorf("%s moved from %v to %v", name, a, b)
		}
	}
	if d := device(t, p2, "b", "peer-b0"); d.RoutedIP != ip("198.18.0.3") {
		t.Errorf("peer-b0 = %v, want next free 198.18.0.3", d.RoutedIP)
	}

	// Once the collision is gone, the auto-mapping is dropped.
	tn = lab()
	tn[0].Peers = tn[0].Peers[1:] // peer-a1 leaves
	p3 := Compute(tn, p1.Mappings, opt)
	if d := device(t, p3, "b", "peer-b1"); d.RoutedIP != ip("100.64.0.1") || d.Mapping != nil {
		t.Errorf("peer-b1 = %+v, want its real IP again", d)
	}

	// Turning auto-mapping off removes the auto-mappings.
	if p4 := Compute(lab(), p1.Mappings, Options{Pool: pool}); len(p4.Mappings) != 0 {
		t.Errorf("mappings with AutoMap off = %+v", p4.Mappings)
	}
}

func TestVanishedPeerKeepsManualMapping(t *testing.T) {
	m := []Mapping{{Tailnet: "b", PeerID: "42", IP: ip("10.77.0.1")}}
	p := Compute(lab(), m, Options{Pool: pool})
	if len(p.Mappings) != 1 {
		t.Errorf("mapping of vanished peer dropped")
	}
	if _, ok := p.Routes[ip("10.77.0.1")]; ok {
		t.Error("mapping of vanished peer is routed")
	}
}

func TestNameCollisionPriority(t *testing.T) {
	tn := lab()
	tn[1].Domain = "a.lab" // scenario dns-suffix
	tn[1].Peers = []Peer{peer("1", "peer-a1", "a.lab", "100.64.0.7"), peer("2", "peer-x", "a.lab", "100.64.0.8")}
	p := Compute(tn, nil, Options{AutoMap: true, Pool: pool})
	if got := p.Names["peer-a1.a.lab."]; got != ip("100.64.0.1") {
		t.Errorf("peer-a1.a.lab -> %v, want tailnet a's device", got)
	}
	if got := p.Names["peer-x.a.lab."]; got != ip("100.64.0.8") {
		t.Errorf("peer-x.a.lab -> %v", got)
	}
	cs := conflicts(p, ConflictName)
	if len(cs) != 1 || cs[0].Key != "peer-a1.a.lab." || len(cs[0].Devices) != 2 {
		t.Errorf("name conflicts = %+v", cs)
	}
	if !slices.Equal(p.Domains, []string{"a.lab"}) {
		t.Errorf("domains = %v", p.Domains)
	}
}

func TestSuggest(t *testing.T) {
	p := Compute(lab(), nil, Options{AutoMap: true, Pool: pool})
	if got := p.Suggest(pool); got != ip("198.18.0.3") {
		t.Errorf("Suggest = %v", got)
	}
	if p.Free(ip("198.18.0.1")) || p.Free(ip("100.64.0.4")) || !p.Free(ip("10.1.2.3")) {
		t.Error("Free gives wrong answers")
	}
	if got := Compute(lab(), nil, Options{AutoMap: true, Pool: netip.MustParsePrefix("198.18.0.0/31")}); len(conflicts(got, ConflictIP)) != 2 ||
		got.Conflicts[0].Resolved {
		t.Errorf("exhausted pool not reported: %+v", got.Conflicts)
	}
}
