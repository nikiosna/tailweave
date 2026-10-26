//go:build lab

package daemon_test

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/Nikiosna/tailweave/internal/daemon"
	"github.com/Nikiosna/tailweave/internal/labtest"
	"github.com/Nikiosna/tailweave/internal/weave"
)

// internet answers with the address it was reached from: the wan address of the exit
// node used, 203.0.113.2 for exit-a, 203.0.113.3 for exit-b. Without an exit node, this
// machine has no route there.
const internet = "198.51.100.10"

// TestExitNode checks the exit-nodes scenario: one exit node at a time takes this
// machine's internet traffic, the tailnets and the LAN stay as they are, and traffic is
// blocked rather than sent directly while the exit node is not available.
func TestExitNode(t *testing.T) {
	labtest.RequireLab(t)
	labtest.RequireScenario(t, "exit-nodes")
	tw := startTailweave(t, t.TempDir())
	tw.login(t, labtest.TailnetA)
	tw.login(t, labtest.TailnetB)
	st := waitState(t, tw, func(s daemon.State) bool {
		a, b := device(s, "a", "exit-a"), device(s, "b", "exit-b")
		return a != nil && a.ExitNode && b != nil && b.ExitNode && device(s, "b", "peer-b1") != nil
	})
	if d := device(st, "a", "peer-a1"); d == nil || d.ExitNode {
		t.Errorf("peer-a1 = %+v, want it without exit node", d)
	}
	if got := curl(t, internet); got != "" {
		t.Fatalf("reached %s without exit node: %q", internet, got)
	}
	if exitRouted(t) {
		t.Error("exit routing set up without an exit node")
	}

	if out := tw.cli(t, "set", "-exit-node=a/exit-a"); !strings.Contains(out, "through exit node exit-a (a)") {
		t.Errorf("set -exit-node: %s", out)
	}
	if got := waitCurl(t, internet, "203.0.113.2"); got != "203.0.113.2" {
		t.Fatalf("curl %s through exit-a = %q", internet, got)
	}
	if !exitRouted(t) {
		t.Error("no exit routing with an exit node")
	}

	t.Run("merged", func(t *testing.T) {
		// Both tailnets stay reachable, also the remapped device, as does the LAN, and the
		// control servers, which are on a network of this machine.
		if got := curl(t, "100.64.0.1"); got != "peer-a1" {
			t.Errorf("curl 100.64.0.1 = %q, want peer-a1", got)
		}
		b1 := device(must(t)(tw.State(t.Context())), "b", "peer-b1")
		if got := curl(t, b1.RoutedIP.String()); got != "peer-b1" {
			t.Errorf("curl %s = %q, want peer-b1", b1.RoutedIP, got)
		}
		if !ping(t, netip.MustParseAddr("192.168.104.2")) {
			t.Error("the LAN's gateway does not answer")
		}
		if out, err := labtest.Output("curl", "-sf", "-m5", labtest.LoginServer(labtest.TailnetA)+"/health"); err != nil {
			t.Errorf("headscale-a not reachable: %v %s", err, out)
		}
	})

	tw.cli(t, "set", "-exit-node=b/exit-b")
	if got := waitCurl(t, internet, "203.0.113.3"); got != "203.0.113.3" {
		t.Fatalf("curl %s through exit-b = %q", internet, got)
	}

	t.Run("fail closed", func(t *testing.T) {
		tw.cli(t, "logout", "b")
		s := must(t)(tw.State(t.Context()))
		if s.ExitNode == nil || s.ExitNode.Available || s.ExitNode.Name != "exit-b" {
			t.Errorf("exit node after logout = %+v, want an unavailable exit-b", s.ExitNode)
		}
		if c := conflict(s, weave.ConflictExit, "exit-b"); c == nil || c.Resolved {
			t.Errorf("exit conflict = %+v", c)
		}
		if out := tw.cli(t, "status"); !strings.Contains(out, "internet traffic is blocked") {
			t.Errorf("status:\n%s", out)
		}
		if got := curl(t, internet); got != "" {
			t.Errorf("reached %s with exit-b's tailnet logged out: %q", internet, got)
		}
		tw.login(t, labtest.TailnetB)
		if got := waitCurl(t, internet, "203.0.113.3"); got != "203.0.113.3" {
			t.Errorf("curl %s after logging in again = %q", internet, got)
		}
	})

	t.Run("stop start", func(t *testing.T) {
		tw.cli(t, "stop")
		if s := must(t)(tw.State(t.Context())); s.ExitNode == nil || s.ExitNode.Name != "exit-b" {
			t.Errorf("exit node while stopped = %+v", s.ExitNode)
		}
		if exitRouted(t) {
			t.Error("exit routing left while stopped")
		}
		tw.cli(t, "start")
		if got := waitCurl(t, internet, "203.0.113.3"); got != "203.0.113.3" {
			t.Errorf("curl %s after start = %q", internet, got)
		}
	})

	if out, err := labtest.Output("sudo", "-n", tw.bin, "set", "-socket", tw.Socket, "-exit-node=a/peer-a1"); err == nil ||
		!strings.Contains(out, "does not offer itself as exit node") {
		t.Errorf("peer-a1 as exit node: %v %s", err, out)
	}

	if out := tw.cli(t, "set", "-exit-node="); !strings.Contains(out, "no exit node") {
		t.Errorf("set -exit-node=: %s", out)
	}
	if got := curl(t, internet); got != "" {
		t.Errorf("reached %s after turning the exit node off: %q", internet, got)
	}
	if exitRouted(t) {
		t.Error("exit routing left after turning the exit node off")
	}
	if out, _ := labtest.Output(tw.bin, "exit-node", "-socket", tw.Socket); !strings.Contains(out, "exit-a  a") {
		t.Errorf("exit-node without a terminal:\n%s", out)
	}
}
