package main

import (
	"bytes"
	"context"
	"net/netip"
	"os"
	"path/filepath"
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
		{Name: "b", State: "Down", Error: "connection refused"},
		{Name: "c", State: "NeedsLogin", AuthURL: "http://hs/register/x"},
	},
	Devices: []weave.Device{
		{Tailnet: "a", PeerID: "1", Name: "peer-a1", DNSName: "peer-a1.a.lab.", Online: true,
			RealIP: netip.MustParseAddr("100.64.0.1"), RoutedIP: netip.MustParseAddr("100.64.0.1")},
		{Tailnet: "b", PeerID: "1", Name: "peer-b1", DNSName: "peer-b1.b.lab.",
			RealIP: netip.MustParseAddr("100.64.0.1"), RoutedIP: netip.MustParseAddr("198.18.0.1"),
			Mapping: &weave.Mapping{Tailnet: "b", PeerID: "1", IP: netip.MustParseAddr("198.18.0.1"), Auto: true}},
		{Tailnet: "b", PeerID: "2", Name: "peer-b2", DNSName: "peer-b2.b.lab.", Online: true,
			RealIP: netip.MustParseAddr("100.64.0.2")},
		{Tailnet: "b", PeerID: "3", Name: "peer-b3", DNSName: "peer-b3.b.lab.",
			RealIP: netip.MustParseAddr("100.64.0.3"), RoutedIP: netip.MustParseAddr("100.64.0.3")},
		{Tailnet: "b", PeerID: "4", Name: "router-b", Online: true,
			RealIP: netip.MustParseAddr("100.64.0.4"), RoutedIP: netip.MustParseAddr("100.64.0.4"),
			Routes: []weave.Route{
				{Prefix: netip.MustParsePrefix("10.99.0.0/24"), Routed: netip.MustParsePrefix("198.18.1.0/24"),
					Mapping: &weave.Mapping{Tailnet: "b", Route: netip.MustParsePrefix("10.99.0.0/24"),
						Prefix: netip.MustParsePrefix("198.18.1.0/24"), Auto: true}},
				{Prefix: netip.MustParsePrefix("192.168.1.0/24")},
				{Prefix: netip.MustParsePrefix("10.5.0.0/16"), Routed: netip.MustParsePrefix("10.5.0.0/16")},
			}},
	},
	Conflicts: []weave.Conflict{
		{Kind: weave.ConflictIP, Key: "100.64.0.1", Resolved: true},
		{Kind: weave.ConflictIP, Key: "100.64.0.2", Message: "map one of them by hand",
			Devices: []weave.DeviceRef{{Tailnet: "a", PeerID: "2", Name: "peer-a2"}, {Tailnet: "b", PeerID: "2", Name: "peer-b2"}}},
		{Kind: weave.ConflictLocal, Key: "192.168.1.0/24", Message: "not routed: overlaps a network of this machine",
			Devices: []weave.DeviceRef{{Tailnet: "b", PeerID: "4", Name: "router-b"}}},
	},
}

func TestPrintStatus(t *testing.T) {
	status := func(o statusOptions) string {
		var b bytes.Buffer
		printStatus(&b, testState, o)
		return b.String()
	}
	out := status(statusOptions{})
	for _, want := range []string{
		"auto-mapping on, DNS on 100.100.100.100, pool 198.18.0.0/15, 2 unresolved conflicts\n" +
			"  ip 100.64.0.2: peer-a2 (a), peer-b2 (b): map one of them by hand\n",
		"  router-b  100.64.0.4\n" +
			"            198.18.1.0/24 ← 10.99.0.0/24  route, auto\n" +
			"            192.168.1.0/24                route, not routed: overlaps a network of this machine\n" +
			"            10.5.0.0/16                   route\n",
		"dut-a",
		"connection refused",
		"log in at http://hs/register/x",
		"\nb  Down        -      -      -           connection refused\n",
		"1 of 1 online",
		"  peer-a1   100.64.0.1\n",
		"  peer-b1   198.18.0.1 ← 100.64.0.1       auto, offline\n",
		"  peer-b2   100.64.0.2                    not routed, same IP as peer-a2 (a)\n",
		"  … 1 offline, show with -a\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("status lacks %q:\n%s", want, out)
		}
	}
	for _, unwanted := range []string{"100.64.0.1:", "peer-b3", "\x1b["} {
		if strings.Contains(out, unwanted) {
			t.Errorf("status shows %q:\n%s", unwanted, out)
		}
	}

	if out := status(statusOptions{all: true}); !strings.Contains(out, "  peer-b3   100.64.0.3                    offline\n") {
		t.Errorf("status -a lacks peer-b3:\n%s", out)
	}
	out = status(statusOptions{tailnet: "a"})
	if strings.Contains(out, "peer-b1") || !strings.Contains(out, "peer-a1") || !strings.Contains(out, "1 unresolved conflict") {
		t.Errorf("status a:\n%s", out)
	}
	if out := status(statusOptions{tailnet: "c"}); !strings.Contains(out, "no conflicts") {
		t.Errorf("status c shows conflicts of other tailnets:\n%s", out)
	}
	if out := status(statusOptions{color: true}); !strings.Contains(out, "\x1b[31m100.64.0.2\x1b[0m") {
		t.Errorf("status without color:\n%q", out)
	}
	if strings.Contains(out, "stopped") {
		t.Errorf("running tailweave shown as stopped:\n%s", out)
	}

	stopped := daemon.State{Stopped: true, Tailnets: []daemon.TailnetState{{Name: "a", State: "Stopped"}}}
	var b bytes.Buffer
	printStatus(&b, stopped, statusOptions{})
	if out := b.String(); !strings.HasPrefix(out, "tailweave is stopped; `tailweave start` switches it on again\n") ||
		!strings.Contains(out, "a  Stopped") {
		t.Errorf("status of a stopped tailweave:\n%s", out)
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

func TestReadAuthKey(t *testing.T) {
	file := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(file, []byte("tskey-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		flag, stdin, want string
		ok                bool
	}{
		{"", "", "", true},
		{"file:" + file, "", "tskey-file", true},
		{"-", " tskey-stdin\n", "tskey-stdin", true},
		{"tskey-literal", "", "tskey-literal", true},
		{" ", "", "", false},
		{"-", "\n", "", false},
		{"file:/nonexistent", "", "", false},
	} {
		got, err := readAuthKey(tc.flag, strings.NewReader(tc.stdin))
		if got != tc.want || (err == nil) != tc.ok {
			t.Errorf("readAuthKey(%q) = %q, %v", tc.flag, got, err)
		}
	}
}

func TestWaitLogin(t *testing.T) {
	states := []daemon.TailnetState{
		{Name: "a", State: "Starting"},
		{Name: "a", State: "NeedsLogin", AuthURL: "http://hs/register/1"},
		{Name: "a", State: "NeedsLogin", AuthURL: "http://hs/register/1"},
		{Name: "a", State: "Running", Self: "dut", IP: netip.MustParseAddr("100.64.0.9")},
	}
	n := 0
	state := func(context.Context) (daemon.State, error) {
		s := daemon.State{Tailnets: []daemon.TailnetState{{Name: "b", State: "Running"}, states[min(n, len(states)-1)]}}
		n++
		return s, nil
	}
	var out bytes.Buffer
	if err := waitLogin(context.Background(), state, "a", &out); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "http://hs/register/1") != 1 || !strings.Contains(out.String(), "logged in to a as dut (100.64.0.9)") {
		t.Errorf("output:\n%s", out.String())
	}

	failed := func(context.Context) (daemon.State, error) {
		return daemon.State{Tailnets: []daemon.TailnetState{{Name: "a", State: "NeedsLogin", LoginError: "invalid key"}}}, nil
	}
	if err := waitLogin(context.Background(), failed, "a", &out); err == nil || !strings.Contains(err.Error(), "invalid key") {
		t.Errorf("err = %v", err)
	}
}

func TestParseInterspersed(t *testing.T) {
	fs, socket := flags("login")
	key := fs.String("auth-key", "", "")
	if err := parse(fs, []string{"a", "-auth-key", "file:k", "-socket=/s"}, 1); err != nil {
		t.Fatal(err)
	}
	if fs.Arg(0) != "a" || fs.NArg() != 1 || *key != "file:k" || *socket != "/s" {
		t.Errorf("args %v, key %q, socket %q", fs.Args(), *key, *socket)
	}
	fs, _ = flags("map")
	if err := parse(fs, []string{"b", "peer-b1", "198.18.0.9"}, 2); err == nil {
		t.Error("three arguments accepted where two are expected")
	}
}
