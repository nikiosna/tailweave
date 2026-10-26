package main

import (
	"bytes"
	"net/netip"
	"strings"
	"testing"

	"github.com/Nikiosna/tailweave/internal/daemon"
	"github.com/Nikiosna/tailweave/internal/weave"
)

// exitState has an exit node in each tailnet, exit-b in use.
var exitState = daemon.State{
	Tailnets: []daemon.TailnetState{{Name: "a", State: "Running"}, {Name: "b", State: "Running"}},
	Devices: []weave.Device{
		{Tailnet: "a", PeerID: "1", Name: "peer-a1", Online: true,
			RealIP: netip.MustParseAddr("100.64.0.1"), RoutedIP: netip.MustParseAddr("100.64.0.1")},
		{Tailnet: "a", PeerID: "4", Name: "exit-a", ExitNode: true,
			RealIP: netip.MustParseAddr("100.64.0.4"), RoutedIP: netip.MustParseAddr("100.64.0.4")},
		{Tailnet: "b", PeerID: "4", Name: "exit-b", ExitNode: true, Online: true,
			RealIP: netip.MustParseAddr("100.64.0.4"), RoutedIP: netip.MustParseAddr("198.18.0.1")},
	},
	ExitNode: &weave.Exit{Tailnet: "b", PeerID: "4", Name: "exit-b", IP: netip.MustParseAddr("100.64.0.4"), Available: true},
}

func TestStatusExitNode(t *testing.T) {
	var b bytes.Buffer
	printStatus(&b, exitState, statusOptions{all: true})
	out := b.String()
	for _, want := range []string{
		"internet traffic goes through exit node exit-b (b), 100.64.0.4\n",
		"  exit-a   100.64.0.4               exit node, offline\n",
		"  exit-b   198.18.0.1 ← 100.64.0.4  exit node in use\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}

	s := exitState
	s.ExitNode = &weave.Exit{Tailnet: "a", PeerID: "4", Name: "exit-a", IP: netip.MustParseAddr("100.64.0.4")}
	b.Reset()
	printStatus(&b, s, statusOptions{})
	out = b.String()
	// Even offline and without -a, the exit node in use shows.
	for _, want := range []string{"exit node exit-a (a) is not available; internet traffic is blocked", "exit node in use, offline"} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
}

func TestPicker(t *testing.T) {
	p := newPicker(exitState)
	if len(p.items) != 3 || p.cur != 2 {
		t.Fatalf("items = %+v, cur = %d; want none, exit-a, exit-b with exit-b highlighted", p.items, p.cur)
	}
	want := []string{
		"> none                   internet traffic leaves directly",
		"  exit-a  a  100.64.0.4  offline",
		"  exit-b  b  100.64.0.4  in use",
	}
	p.cur = 0
	if got := p.rows(true); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	for _, tc := range []struct {
		key      string
		cur      int
		done, ok bool
	}{
		{"\x1b[B", 1, false, false},
		{"j", 2, false, false},
		{"\x1b[B", 0, false, false}, // wraps around
		{"\x1b[A", 2, false, false},
		{"k", 1, false, false},
		{"x", 1, false, false},
		{"\r", 1, true, true},
		{"q", 1, true, false},
		{"\x1b", 1, true, false},
		{"\x03", 1, true, false},
	} {
		done, ok := p.key([]byte(tc.key))
		if p.cur != tc.cur || done != tc.done || ok != tc.ok {
			t.Errorf("key %q: cur %d, done %v, ok %v; want %d, %v, %v", tc.key, p.cur, done, ok, tc.cur, tc.done, tc.ok)
		}
	}

	// No exit node in use: "none" is; a picked exit node that is no longer offered stays
	// in the list.
	s := exitState
	s.ExitNode = nil
	if p := newPicker(s); p.cur != 0 || !p.items[0].inUse {
		t.Errorf("without exit node: cur %d, items %+v", p.cur, p.items)
	}
	s.ExitNode = &weave.Exit{Tailnet: "a", PeerID: "1", Name: "peer-a1"}
	if p := newPicker(s); len(p.items) != 4 || !strings.Contains(p.rows(false)[p.cur], "no longer offered, in use") {
		t.Errorf("withdrawn exit node: %q", p.rows(false))
	}

	var b bytes.Buffer
	newPicker(daemon.State{}).list(&b)
	if b.String() != "no tailnet offers an exit node\n" {
		t.Errorf("list without exit nodes: %q", b.String())
	}
}
