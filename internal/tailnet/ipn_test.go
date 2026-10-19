package tailnet

import (
	"net/netip"
	"slices"
	"testing"

	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
	"tailscale.com/types/key"
	"tailscale.com/types/views"
)

func TestFromIPN(t *testing.T) {
	routes := []netip.Prefix{netip.MustParsePrefix("10.99.0.0/24"), netip.MustParsePrefix("0.0.0.0/0")}
	pr := views.SliceOf(routes)
	st := &ipnstate.Status{
		BackendState: "Running", MagicDNSSuffix: "a.lab",
		CurrentTailnet: &ipnstate.TailnetStatus{MagicDNSEnabled: true},
		Self:           &ipnstate.PeerStatus{ID: "9", DNSName: "dut-a.a.lab.", TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.9")}},
		Peer: map[key.NodePublic]*ipnstate.PeerStatus{
			key.NewNode().Public(): {ID: tailcfg.StableNodeID("3"), DNSName: "router-a.a.lab.", Online: true,
				TailscaleIPs: []netip.Addr{netip.MustParseAddr("100.64.0.3")}, PrimaryRoutes: &pr},
		},
	}
	tn := FromIPN("a", st).Tailnet
	if len(tn.Peers) != 1 || tn.Domain != "a.lab" {
		t.Fatalf("tailnet = %+v", tn)
	}
	if r := tn.Peers[0].Routes; !slices.Equal(r, routes[:1]) {
		t.Errorf("routes = %v, want only the subnet route", r)
	}
}
