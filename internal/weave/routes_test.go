package weave

import (
	"net/netip"
	"slices"
	"testing"
)

func pfx(s string) netip.Prefix { return netip.MustParsePrefix(s) }

var (
	lan   = pfx("192.168.104.0/24")
	via7  = pfx("fd7a:115c:a1e0:b1a:0:7:a63:0/120")
	via8  = pfx("fd7a:115c:a1e0:b1a:0:8:a63:0/120")
	cgnat = pfx("100.64.0.0/10")
)

// subnetLab mirrors the lab's subnets scenario: both tailnets route 10.99.0.0/24, b also
// routes the LAN of this machine, and each has a 4via6 route for the same /24.
func subnetLab() []Tailnet {
	tns := lab()
	ra := peer("3", "router-a", "a.lab", "100.64.0.5")
	ra.Routes = []netip.Prefix{pfx("10.99.0.0/24"), via7, pfx("0.0.0.0/0")}
	rb := peer("4", "router-b", "b.lab", "100.64.0.6")
	rb.Routes = []netip.Prefix{lan, via8, pfx("10.99.0.0/24")}
	tns[0].Peers = append(tns[0].Peers, ra)
	tns[1].Peers = append(tns[1].Peers, rb)
	return tns
}

func opts(autoMap bool) Options {
	return Options{AutoMap: autoMap, Pool: pool, Exclude: []netip.Prefix{cgnat, lan}}
}

func route(t *testing.T, p Plan, tailnet, name string, r netip.Prefix) Route {
	t.Helper()
	for _, x := range device(t, p, tailnet, name).Routes {
		if x.Prefix == r {
			return x
		}
	}
	t.Fatalf("route %s of %s/%s not in plan", r, tailnet, name)
	return Route{}
}

func TestSubnetRouted(t *testing.T) {
	p := Compute(subnetLab(), nil, opts(true))
	if got := p.Subnets[pfx("10.99.0.0/24")]; got != (SubnetTarget{"a", pfx("10.99.0.0/24")}) {
		t.Errorf("10.99.0.0/24 -> %v, want tailnet a", got)
	}
	if got := route(t, p, "a", "router-a", pfx("10.99.0.0/24")); got.Routed != pfx("10.99.0.0/24") || got.Mapping != nil {
		t.Errorf("router-a route = %+v", got)
	}
	if n := len(device(t, p, "a", "router-a").Routes); n != 2 {
		t.Errorf("router-a has %d routes, want 2 (exit route ignored)", n)
	}
	if _, ok := p.Subnets[pfx("0.0.0.0/0")]; ok {
		t.Error("exit route is routed")
	}
}

func TestSubnetCollisionAutoMap(t *testing.T) {
	p := Compute(subnetLab(), nil, opts(true))

	r := route(t, p, "b", "router-b", pfx("10.99.0.0/24"))
	if r.Mapping == nil || !r.Mapping.Auto || r.Routed.Bits() != 24 || !pool.Contains(r.Routed.Addr()) {
		t.Fatalf("router-b route = %+v, want an auto-mapped /24 from the pool", r)
	}
	if r.Routed.Masked() != r.Routed {
		t.Errorf("block %s is not aligned", r.Routed)
	}
	if got := p.Subnets[r.Routed]; got != (SubnetTarget{"b", pfx("10.99.0.0/24")}) {
		t.Errorf("%s -> %v", r.Routed, got)
	}
	// The block must not hold the devices' auto-mapped IPs.
	for _, d := range p.Devices {
		if d.Mapping != nil && r.Routed.Contains(d.RoutedIP) {
			t.Errorf("block %s holds %s of %s", r.Routed, d.RoutedIP, d.Name)
		}
	}
	cs := conflicts(p, ConflictRoute)
	if len(cs) != 1 || !cs[0].Resolved || cs[0].Key != "10.99.0.0/24" || len(cs[0].Devices) != 2 {
		t.Errorf("route conflicts = %+v", cs)
	}
	if !slices.Contains(p.Mappings, *r.Mapping) {
		t.Errorf("auto mapping not stored: %v", p.Mappings)
	}
}

func TestSubnetCollisionSkipped(t *testing.T) {
	p := Compute(subnetLab(), nil, opts(false))
	if r := route(t, p, "b", "router-b", pfx("10.99.0.0/24")); r.Routed.IsValid() {
		t.Errorf("router-b route = %+v, want unrouted", r)
	}
	if len(p.Subnets) != 3 { // 10.99.0.0/24 of a and both 4via6 routes
		t.Errorf("subnets = %v", p.Subnets)
	}
	cs := conflicts(p, ConflictRoute)
	if len(cs) != 1 || cs[0].Resolved {
		t.Errorf("route conflicts = %+v, want one unresolved", cs)
	}
}

func TestSubnetPartialOverlap(t *testing.T) {
	tns := subnetLab()
	tns[1].Peers[3].Routes = []netip.Prefix{pfx("10.99.0.128/25"), pfx("10.99.0.0/16")}
	p := Compute(tns, nil, opts(true))
	for _, r := range []netip.Prefix{pfx("10.99.0.128/25"), pfx("10.99.0.0/16")} {
		got := route(t, p, "b", "router-b", r)
		if got.Mapping == nil || got.Routed.Bits() != r.Bits() {
			t.Errorf("route %s = %+v, want a remapped block of the same size", r, got)
		}
	}
	// Overlaps within one tailnet are fine: both go to the same place.
	tns = subnetLab()
	tns[0].Peers[0].Routes = []netip.Prefix{pfx("10.99.0.0/16")}
	p = Compute(tns, nil, opts(true))
	if p.Subnets[pfx("10.99.0.0/16")].Tailnet != "a" || p.Subnets[pfx("10.99.0.0/24")].Tailnet != "a" {
		t.Errorf("subnets = %v", p.Subnets)
	}
}

func TestSubnetOverlapsLocal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		route   netip.Prefix
		autoMap bool
	}{
		{"lan", lan, true},
		{"lan inside", pfx("192.168.104.128/25"), true},
		{"cgnat", pfx("100.64.0.0/24"), true},
		{"pool", pfx("198.18.4.0/24"), true},
		{"lan without auto-mapping", lan, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tns := lab()
			r := peer("4", "router-b", "b.lab", "100.64.0.6")
			r.Routes = []netip.Prefix{tc.route}
			tns[1].Peers = append(tns[1].Peers, r)
			p := Compute(tns, nil, opts(tc.autoMap))
			if _, ok := p.Subnets[tc.route]; ok {
				t.Fatalf("%s routed as it is", tc.route)
			}
			got := route(t, p, "b", "router-b", tc.route)
			if tc.autoMap != got.Routed.IsValid() || got.Routed.IsValid() && !pool.Contains(got.Routed.Addr()) {
				t.Errorf("route = %+v", got)
			}
			cs := conflicts(p, ConflictLocal)
			if len(cs) != 1 || cs[0].Resolved != tc.autoMap || cs[0].Key != tc.route.String() {
				t.Errorf("local conflicts = %+v", cs)
			}
		})
	}
}

func TestSubnetMappingStable(t *testing.T) {
	first := Compute(subnetLab(), nil, opts(true))
	b := route(t, first, "b", "router-b", pfx("10.99.0.0/24")).Routed
	// A device auto-mapping may not take the block's addresses on the next run.
	tns := subnetLab()
	tns[1].Peers = append(tns[1].Peers, peer("5", "peer-b5", "b.lab", "100.64.0.2"))
	again := Compute(tns, first.Mappings, opts(true))
	if got := route(t, again, "b", "router-b", pfx("10.99.0.0/24")).Routed; got != b {
		t.Errorf("block moved from %s to %s", b, got)
	}
	for _, d := range again.Devices {
		if b.Contains(d.RoutedIP) {
			t.Errorf("%s got %s inside the block %s", d.Name, d.RoutedIP, b)
		}
	}
	// Without auto-mapping the stored block is dropped.
	off := Compute(subnetLab(), first.Mappings, opts(false))
	for _, m := range off.Mappings {
		if m.Route.IsValid() {
			t.Errorf("auto route mapping kept: %+v", m)
		}
	}
}

func TestManualSubnetMapping(t *testing.T) {
	m := Mapping{Tailnet: "b", Route: pfx("10.99.0.0/24"), Prefix: pfx("10.98.0.0/24")}
	p := Compute(subnetLab(), []Mapping{m}, opts(false))
	if got := route(t, p, "b", "router-b", pfx("10.99.0.0/24")); got.Routed != m.Prefix || got.Mapping == nil || got.Mapping.Auto {
		t.Errorf("router-b route = %+v", got)
	}
	if got := p.Subnets[m.Prefix]; got != (SubnetTarget{"b", pfx("10.99.0.0/24")}) {
		t.Errorf("%s -> %v", m.Prefix, got)
	}
	if cs := conflicts(p, ConflictRoute); len(cs) != 0 {
		t.Errorf("route conflicts = %+v, want none", cs)
	}
	// The mapping stays while the route is gone.
	gone := lab()
	p = Compute(gone, []Mapping{m}, opts(false))
	if !slices.Contains(p.Mappings, m) || len(p.Subnets) != 0 {
		t.Errorf("mappings = %v, subnets = %v", p.Mappings, p.Subnets)
	}
}

func TestSubnetMappingRejected(t *testing.T) {
	route := pfx("10.99.0.0/24")
	for _, tc := range []struct {
		name   string
		prefix netip.Prefix
	}{
		{"other size", pfx("10.98.0.0/23")},
		{"IPv6", pfx("fd00::/120")},
		{"lan", pfx("192.168.104.0/24")},
		{"device IP", pfx("100.64.0.0/24")},
		{"real route", pfx("10.99.0.0/24")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := Mapping{Tailnet: "b", Route: route, Prefix: tc.prefix}
			p := Compute(subnetLab(), []Mapping{m}, opts(false))
			if cs := conflicts(p, ConflictMapping); len(cs) != 1 {
				t.Errorf("mapping conflicts = %+v", cs)
			}
			if _, ok := p.Subnets[tc.prefix]; ok && p.Subnets[tc.prefix].Tailnet == "b" {
				t.Errorf("rejected mapping routed: %v", p.Subnets)
			}
		})
	}
}

func TestPoolSharedByIPsAndSubnets(t *testing.T) {
	small := pfx("198.18.0.0/23") // room for one /24 block next to the device IPs
	o := Options{AutoMap: true, Pool: small, Exclude: []netip.Prefix{cgnat, lan}}
	p := Compute(subnetLab(), nil, o)
	r := route(t, p, "b", "router-b", pfx("10.99.0.0/24"))
	if r.Routed != pfx("198.18.1.0/24") {
		t.Fatalf("block = %s, want 198.18.1.0/24 next to the device IPs in 198.18.0.0/24", r.Routed)
	}
	// LAN route needs another /24 that is not there.
	cs := conflicts(p, ConflictLocal)
	if len(cs) != 1 || cs[0].Resolved || cs[0].Message != "address pool exhausted, some routes are not routed" {
		t.Errorf("local conflicts = %+v", cs)
	}
	if p.Free(ip("198.18.1.7")) || p.FreePrefix(pfx("198.18.1.128/25")) {
		t.Error("block reported as free")
	}
	if got := p.SuggestPrefix(pool, 24); got != pfx("198.18.2.0/24") {
		t.Errorf("suggested block = %s", got)
	}
}

func TestViaRouted(t *testing.T) {
	p := Compute(subnetLab(), nil, opts(true))
	if p.Subnets[via7].Tailnet != "a" || p.Subnets[via8].Tailnet != "b" {
		t.Errorf("subnets = %v", p.Subnets)
	}
	if !slices.Equal(p.Via["a.lab"], []netip.Prefix{via7}) || !slices.Equal(p.Via["b.lab"], []netip.Prefix{via8}) {
		t.Errorf("via = %v", p.Via)
	}
}

func TestViaCollisionFirstWins(t *testing.T) {
	for _, autoMap := range []bool{true, false} {
		tns := subnetLab()
		tns[1].Peers[3].Routes = []netip.Prefix{via7}
		p := Compute(tns, nil, opts(autoMap))
		if p.Subnets[via7].Tailnet != "a" {
			t.Errorf("auto %v: %s -> %v", autoMap, via7, p.Subnets[via7])
		}
		if r := route(t, p, "b", "router-b", via7); r.Routed.IsValid() {
			t.Errorf("auto %v: router-b via = %+v, want unrouted", autoMap, r)
		}
		cs := conflicts(p, ConflictRoute)
		if len(cs) != 1 || cs[0].Resolved {
			t.Errorf("auto %v: route conflicts = %+v", autoMap, cs)
		}
	}
}

func TestRoutable(t *testing.T) {
	for s, want := range map[string]bool{
		"10.0.0.0/8": true, "0.0.0.0/0": false, "::/0": false, "fd00::/64": false,
		"fd7a:115c:a1e0:b1a:0:7:a63:0/120": true, "fd7a:115c:a1e0::/48": false,
	} {
		if got := Routable(pfx(s)); got != want {
			t.Errorf("Routable(%s) = %v", s, got)
		}
	}
}
