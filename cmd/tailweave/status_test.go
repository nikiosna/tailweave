package main

import (
	"bytes"
	"net/netip"
	"strings"
	"testing"

	"github.com/Nikiosna/tailweave/internal/daemon"
	"github.com/Nikiosna/tailweave/internal/weave"
)

var testState = daemon.State{
	AutoMap: true,
	DNS:     netip.MustParseAddr("100.100.100.100"),
	Pool:    netip.MustParsePrefix("198.18.0.0/15"),
	Tailnets: []daemon.TailnetState{
		{Name: "a", Domain: "a.lab", Self: "dut-a", IP: netip.MustParseAddr("100.64.0.5")},
		{Name: "b", Error: "connection refused"},
	},
	Devices: []weave.Device{
		{Tailnet: "a", PeerID: "1", Name: "peer-a1", DNSName: "peer-a1.a.lab.", Online: true,
			RealIP: netip.MustParseAddr("100.64.0.1"), RoutedIP: netip.MustParseAddr("100.64.0.1")},
		{Tailnet: "b", PeerID: "1", Name: "peer-b1", DNSName: "peer-b1.b.lab.",
			RealIP: netip.MustParseAddr("100.64.0.1"), RoutedIP: netip.MustParseAddr("198.18.0.1"),
			Mapping: &weave.Mapping{Tailnet: "b", PeerID: "1", IP: netip.MustParseAddr("198.18.0.1"), Auto: true}},
		{Tailnet: "b", PeerID: "2", Name: "peer-b2", DNSName: "peer-b2.b.lab.", Online: true,
			RealIP: netip.MustParseAddr("100.64.0.2")},
	},
	Conflicts: []weave.Conflict{
		{Kind: weave.ConflictIP, Key: "100.64.0.1", Resolved: true},
		{Kind: weave.ConflictIP, Key: "100.64.0.2", Message: "map one of them by hand",
			Devices: []weave.DeviceRef{{Tailnet: "a", Name: "peer-a2"}, {Tailnet: "b", Name: "peer-b2"}}},
	},
}

func TestPrintStatus(t *testing.T) {
	var b bytes.Buffer
	printStatus(&b, testState)
	out := b.String()
	for _, want := range []string{
		"auto-mapping on, DNS on 100.100.100.100, pool 198.18.0.0/15",
		"dut-a",
		"connection refused",
		"peer-b1 (offline)",
		"198.18.0.1",
		"auto",
		"not routed",
		"ip 100.64.0.2: peer-a2 (a), peer-b2 (b): map one of them by hand",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "100.64.0.1:") {
		t.Errorf("resolved conflict listed:\n%s", out)
	}
}

func TestPickDevice(t *testing.T) {
	for _, tc := range []struct{ tailnet, device, want string }{
		{"b", "peer-b1", "1"},
		{"b", "PEER-B2", "2"},
		{"b", "peer-b2.b.lab", "2"},
		{"b", "2", "2"},
		{"a", "peer-a1", "1"},
	} {
		d, err := pickDevice(testState, tc.tailnet, tc.device)
		if err != nil || d.Tailnet != tc.tailnet || d.PeerID != tc.want {
			t.Errorf("pickDevice(%s, %s) = %s/%s, %v; want peer %s", tc.tailnet, tc.device, d.Tailnet, d.PeerID, err, tc.want)
		}
	}
	if _, err := pickDevice(testState, "a", "peer-b1"); err == nil {
		t.Error("found peer-b1 in tailnet a")
	}
}
