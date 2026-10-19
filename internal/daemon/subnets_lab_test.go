//go:build lab

package daemon_test

import (
	"net/netip"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/Nikiosna/tailweave/internal/daemon"
	"github.com/Nikiosna/tailweave/internal/labtest"
	"github.com/Nikiosna/tailweave/internal/weave"
)

var (
	labSubnet = netip.MustParsePrefix("10.99.0.0/24")     // routed by router-a and router-b
	limaLAN   = netip.MustParsePrefix("192.168.104.0/24") // the VMs' LAN, also routed by router-b
)

// TestSubnets checks the subnets scenario: both tailnets route 10.99.0.0/24, where each
// router answers with its own name on 10.99.0.1, and router-b also routes this machine's
// LAN.
func TestSubnets(t *testing.T) {
	labtest.RequireLab(t)
	labtest.RequireScenario(t, "subnets")
	tw := startTailweave(t, t.TempDir())
	tw.login(t, labtest.TailnetA)
	tw.login(t, labtest.TailnetB)
	st := waitState(t, tw, func(s daemon.State) bool {
		return routeOf(s, "b", labSubnet).Routed.IsValid() && routeOf(s, "b", limaLAN).Prefix.IsValid()
	})

	t.Run("routed", func(t *testing.T) {
		if r := routeOf(st, "a", labSubnet); r.Routed != labSubnet || r.Mapping != nil {
			t.Errorf("router-a's route = %+v, want routed as it is", r)
		}
		if got := waitCurl(t, "10.99.0.1", "router-a"); got != "router-a" {
			t.Errorf("curl 10.99.0.1 = %q, want router-a (tailnet a keeps colliding routes)", got)
		}
	})

	t.Run("remapped", func(t *testing.T) {
		r := routeOf(st, "b", labSubnet)
		if r.Mapping == nil || !r.Mapping.Auto || r.Routed.Bits() != 24 {
			t.Fatalf("router-b's route = %+v, want an auto-mapped /24", r)
		}
		if got := waitCurl(t, hostIn(r.Routed, 1).String(), "router-b"); got != "router-b" {
			t.Errorf("curl %s = %q, want router-b", hostIn(r.Routed, 1), got)
		}
		if c := conflict(st, weave.ConflictRoute, labSubnet.String()); c == nil || !c.Resolved {
			t.Errorf("route conflict = %+v, want a resolved one", c)
		}
	})

	// The LAN stays where it is; router-b's route to it is reachable at a pool block.
	t.Run("lan", func(t *testing.T) {
		r := routeOf(st, "b", limaLAN)
		if r.Mapping == nil || r.Routed == limaLAN || !r.Routed.IsValid() {
			t.Fatalf("router-b's LAN route = %+v, want it remapped", r)
		}
		if c := conflict(st, weave.ConflictLocal, limaLAN.String()); c == nil || !c.Resolved {
			t.Errorf("local conflict = %+v, want a resolved one", c)
		}
		if !ping(t, netip.MustParseAddr("192.168.104.2")) {
			t.Error("the LAN's gateway does not answer")
		}
		// Through router-b, the Linux VM (192.168.104.1) answers in the block.
		if vm := hostIn(r.Routed, 1); !ping(t, vm) {
			t.Errorf("%s (the Linux VM via router-b) does not answer", vm)
		}
	})

	t.Run("manual", func(t *testing.T) {
		s := must(t)(tw.SetAutoMap(t.Context(), false))
		if r := routeOf(s, "b", labSubnet); r.Routed.IsValid() {
			t.Errorf("router-b's route still routed to %s without auto-mapping", r.Routed)
		}
		if c := conflict(s, weave.ConflictRoute, labSubnet.String()); c == nil || c.Resolved {
			t.Errorf("route conflict = %+v, want an unresolved one", c)
		}

		block := "198.18.200.0/24"
		if out := tw.cli(t, "map", "b", labSubnet.String(), block); !strings.Contains(out, "reachable at "+block) {
			t.Errorf("map: %s", out)
		}
		if got := waitCurl(t, "198.18.200.1", "router-b"); got != "router-b" {
			t.Errorf("curl 198.18.200.1 = %q, want router-b", got)
		}
		if got := curl(t, "10.99.0.1"); got != "router-a" {
			t.Errorf("curl 10.99.0.1 after mapping b's route away = %q", got)
		}
		if out := tw.cli(t, "status"); !strings.Contains(out, block) {
			t.Errorf("status lacks %s:\n%s", block, out)
		}
		if out := tw.cli(t, "unmap", "b", labSubnet.String()); !strings.Contains(out, "not routed") {
			t.Errorf("unmap: %s", out)
		}
		must(t)(tw.SetAutoMap(t.Context(), true))
	})
}

// TestVia checks 4via6 in the subnets scenario: router-a routes 10.99.0.0/24 as site 7,
// router-b the same /24 as site 8, so both are reachable at once, by IPv6 address and by
// MagicDNS name.
func TestVia(t *testing.T) {
	labtest.RequireLab(t)
	labtest.RequireScenario(t, "subnets")
	tw := startTailweave(t, t.TempDir())
	tw.login(t, labtest.TailnetA)
	tw.login(t, labtest.TailnetB)
	via7 := netip.MustParsePrefix("fd7a:115c:a1e0:b1a:0:7:a63:0/120")
	via8 := netip.MustParsePrefix("fd7a:115c:a1e0:b1a:0:8:a63:0/120")
	waitState(t, tw, func(s daemon.State) bool {
		return routeOf(s, "a", via7).Routed == via7 && routeOf(s, "b", via8).Routed == via8
	})

	for _, tc := range []struct{ host, want string }{
		{"[fd7a:115c:a1e0:b1a:0:7:a63:1]", "router-a"},
		{"[fd7a:115c:a1e0:b1a:0:8:a63:1]", "router-b"},
	} {
		if got := waitCurl(t, tc.host, tc.want); got != tc.want {
			t.Errorf("curl %s = %q, want %q", tc.host, got, tc.want)
		}
	}
	if !resolvesAAAA(t, "a.lab") {
		t.Skip("the system resolver does not ask for AAAA records here (macOS without IPv6 connectivity)")
	}
	for _, tc := range []struct{ host, want string }{
		{"10-99-0-1-via-7.a.lab", "router-a"},
		{"10-99-0-1-via-8.b.lab", "router-b"},
	} {
		if got := waitCurl(t, tc.host, tc.want); got != tc.want {
			t.Errorf("curl %s = %q, want %q", tc.host, got, tc.want)
		}
	}
	// Site 8 belongs to tailnet b; a's domain does not know it.
	if out, ok := resolve(t, "10-99-0-1-via-8.a.lab"); ok {
		t.Errorf("10-99-0-1-via-8.a.lab resolves: %s", out)
	}
}

// routeOf returns a tailnet's subnet route as the state shows it, or the zero Route.
func routeOf(s daemon.State, tailnet string, p netip.Prefix) weave.Route {
	for _, d := range s.Devices {
		for _, r := range d.Routes {
			if d.Tailnet == tailnet && r.Prefix == p {
				return r
			}
		}
	}
	return weave.Route{}
}

func conflict(s daemon.State, kind, key string) *weave.Conflict {
	for i, c := range s.Conflicts {
		if c.Kind == kind && c.Key == key {
			return &s.Conflicts[i]
		}
	}
	return nil
}

// hostIn returns the n-th address of p.
func hostIn(p netip.Prefix, n int) netip.Addr {
	a := p.Masked().Addr()
	for range n {
		a = a.Next()
	}
	return a
}

// ping reports whether ip answers an ICMP echo within 20 seconds.
func ping(t *testing.T, ip netip.Addr) bool {
	t.Helper()
	wait := "-W" // seconds on Linux, milliseconds on macOS
	timeout := "2"
	if runtime.GOOS == "darwin" {
		timeout = "2000"
	}
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(time.Second) {
		if exec.Command("ping", "-c1", wait, timeout, ip.String()).Run() == nil {
			return true
		}
	}
	return false
}
