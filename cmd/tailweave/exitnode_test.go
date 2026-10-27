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
	if len(p.nodes) != 3 || p.cur != 2 {
		t.Fatalf("nodes = %d, cur = %d; want none, exit-a, exit-b with exit-b highlighted", len(p.nodes), p.cur)
	}
	want := []string{
		"> none                   internet traffic leaves directly",
		"  exit-a  a  100.64.0.4  offline",
		"  exit-b  b  100.64.0.4  in use",
	}
	p.cur = 0
	if got := p.rows(p.visible(), true); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	for _, tc := range []struct {
		key  string
		cur  int
		done bool
		pick string // name of the picked exit node, "" for none
	}{
		{"\x1b[B", 1, false, ""},
		{"j", 2, false, ""},
		{"\x1b[B", 0, false, ""}, // wraps around
		{"\x1b[A", 2, false, ""},
		{"k", 1, false, ""},
		{"x", 1, false, ""},
		{"\x1b[C", 1, false, ""}, // nothing to open
		{"\r", 1, true, "exit-a"},
		{"q", 1, true, ""},
		{"\x1b", 1, true, ""},
		{"\x03", 1, true, ""},
	} {
		done, pick := p.key([]byte(tc.key))
		name := ""
		if pick != nil {
			name = pick.dev.Name
		}
		if p.cur != tc.cur || done != tc.done || name != tc.pick {
			t.Errorf("key %q: cur %d, done %v, pick %q; want %d, %v, %q", tc.key, p.cur, done, name, tc.cur, tc.done, tc.pick)
		}
	}

	// No exit node in use: "none" is; a picked exit node that is no longer offered stays
	// in the list.
	s := exitState
	s.ExitNode = nil
	if p := newPicker(s); p.cur != 0 || !p.nodes[0].item.inUse {
		t.Errorf("without exit node: cur %d", p.cur)
	}
	s.ExitNode = &weave.Exit{Tailnet: "a", PeerID: "1", Name: "peer-a1"}
	if p := newPicker(s); len(p.nodes) != 4 || !strings.Contains(p.rows(p.visible(), false)[p.cur], "no longer offered, in use") {
		t.Errorf("withdrawn exit node: %q", p.rows(p.visible(), false))
	}

	var b bytes.Buffer
	newPicker(daemon.State{}).list(&b)
	if b.String() != "no tailnet offers an exit node\n" {
		t.Errorf("list without exit nodes: %q", b.String())
	}
}

// mullvadState has exit nodes with a location, like Mullvad's, next to one without;
// de-ber-wg-002 is in use.
func mullvadState() daemon.State {
	s := daemon.State{Tailnets: []daemon.TailnetState{{Name: "home", State: "Running"}}}
	add := func(name, ip, country, city string, prio int, online bool) {
		d := weave.Device{Tailnet: "home", PeerID: name, Name: name, Online: online, ExitNode: true,
			RealIP: netip.MustParseAddr(ip), RoutedIP: netip.MustParseAddr(ip)}
		if country != "" {
			d.Location = &weave.Location{Country: country, City: city, Priority: prio}
		}
		s.Devices = append(s.Devices, d)
	}
	add("diskstation", "100.98.195.117", "", "", 0, true)
	add("se-mma-wg-001", "100.66.72.109", "Sweden", "Malmö", 0, true)
	add("de-fra-wg-001", "100.120.246.41", "Germany", "Frankfurt", 10, true)
	add("de-fra-wg-002", "100.121.154.59", "Germany", "Frankfurt", 50, false)
	add("de-fra-wg-003", "100.100.45.89", "Germany", "Frankfurt", 20, true)
	add("de-ber-wg-001", "100.123.112.108", "Germany", "Berlin", 0, true)
	add("de-ber-wg-002", "100.65.150.10", "Germany", "Berlin", 0, true)
	s.ExitNode = &weave.Exit{Tailnet: "home", PeerID: "de-ber-wg-002", Name: "de-ber-wg-002", Available: true}
	return s
}

func TestPickerByPlace(t *testing.T) {
	p := newPicker(mullvadState())
	// The way to the exit node in use is open, the cursor on it.
	got := p.rows(p.visible(), true)
	wantRows := []string{
		"  none",
		"  diskstation",
		"  ▾ Germany",
		"    ▾ Berlin",
		"        de-ber-wg-001",
		">       de-ber-wg-002",
		"    ▸ Frankfurt",
		"  ▸ Sweden",
	}
	if len(got) != len(wantRows) {
		t.Fatalf("rows:\n%s", strings.Join(got, "\n"))
	}
	for i, w := range wantRows {
		if !strings.HasPrefix(got[i], w+" ") {
			t.Errorf("row %d = %q, want it to start with %q", i, got[i], w)
		}
	}
	for i, w := range map[int]string{2: "5 exit nodes", 3: "de-ber-wg-001 (best of 2)", 5: "in use",
		6: "de-fra-wg-003 (best of 3)", 7: "1 exit node"} {
		if !strings.HasSuffix(got[i], w) {
			t.Errorf("row %d = %q, want it to end with %q", i, got[i], w)
		}
	}

	// Left goes to the city, left again closes it, then Germany shows that it holds the
	// exit node in use.
	p.key([]byte("\x1b[D"))
	p.key([]byte("\x1b[D"))
	p.key([]byte("\x1b[D"))
	p.key([]byte("\x1b[D"))
	if got := p.rows(p.visible(), true); len(got) != 4 || !strings.HasPrefix(got[2], "> ▸ Germany") || !strings.HasSuffix(got[2], "5 exit nodes, in use") {
		t.Errorf("after closing:\n%s", strings.Join(got, "\n"))
	}
	// Enter opens a country; picking a city picks its best exit node, online before priority.
	p.key([]byte("\r"))
	p.key([]byte("j"))
	p.key([]byte("j"))
	if done, pick := p.key([]byte("\r")); !done || pick == nil || pick.dev.Name != "de-fra-wg-003" {
		t.Errorf("Frankfurt picked %+v", pick)
	}

	// Without a terminal, the whole tree shows.
	var b bytes.Buffer
	newPicker(mullvadState()).list(&b)
	out := b.String()
	for _, w := range []string{"    ▾ Frankfurt", "        de-fra-wg-002  home  100.121.154.59   offline\n", "  ▾ Sweden", "      Malmö            home  100.66.72.109    se-mma-wg-001\n"} {
		if !strings.Contains(out, w) {
			t.Errorf("list lacks %q:\n%s", w, out)
		}
	}
}

func TestPickerScroll(t *testing.T) {
	p := newPicker(mullvadState())
	p.height = 3
	var b bytes.Buffer
	p.draw(&b, false)
	lines := strings.Split(strings.TrimSuffix(b.String(), "\r\n"), "\r\n")
	if len(lines) != 4 || !strings.HasSuffix(lines[0], " 6/8:") || !strings.Contains(lines[3], "> ") || !strings.Contains(lines[3], "de-ber-wg-002") {
		t.Errorf("drawn: %q", lines)
	}
	p.key([]byte("g"))
	b.Reset()
	p.draw(&b, false)
	if !strings.HasPrefix(b.String(), "\x1b[4A") || !strings.Contains(b.String(), "> none") || p.top != 0 {
		t.Errorf("after home: top %d, %q", p.top, b.String())
	}
}
