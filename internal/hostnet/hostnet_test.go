package hostnet

import (
	"net/netip"
	"slices"
	"testing"
)

func TestDiffRoutes(t *testing.T) {
	p := netip.MustParsePrefix
	have := []netip.Prefix{p("10.99.0.0/24"), p("10.5.0.0/16")}
	want := []netip.Prefix{p("10.5.0.0/16"), p("192.168.7.0/24"), p("192.168.7.0/24")}
	add, del := diffRoutes(have, want)
	if !slices.Equal(add, []netip.Prefix{p("192.168.7.0/24")}) || !slices.Equal(del, []netip.Prefix{p("10.99.0.0/24")}) {
		t.Errorf("add %v, del %v", add, del)
	}
	if add, del := diffRoutes(nil, nil); add != nil || del != nil {
		t.Errorf("empty: add %v, del %v", add, del)
	}
}
