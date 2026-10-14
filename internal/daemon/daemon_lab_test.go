//go:build lab

package daemon_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nikiosna/tailweave/internal/client"
	"github.com/Nikiosna/tailweave/internal/daemon"
	"github.com/Nikiosna/tailweave/internal/labtest"
	"github.com/Nikiosna/tailweave/internal/weave"
)

// TestWeave runs tailweave, logs it into both lab tailnets with preauth keys and checks
// from the host itself that both are reachable by name and IP, with and without
// auto-mapping.
func TestWeave(t *testing.T) {
	labtest.RequireLab(t)
	tw := startTailweave(t, t.TempDir())
	tw.login(t, labtest.TailnetA)
	tw.login(t, labtest.TailnetB)

	// b's MagicDNS domain is a.lab in the dns-suffix scenario.
	domB := "b.lab"
	if labtest.Scenario() == "dns-suffix" {
		domB = "a.lab"
	}
	st := waitState(t, tw, func(s daemon.State) bool { return device(s, "b", "peer-b3") != nil })

	t.Run("names", func(t *testing.T) {
		for _, tc := range []struct{ host, want string }{
			{"peer-a1.a.lab", "peer-a1"},
			{"peer-b1." + domB, "peer-b1"},
			{"peer-b2", "peer-b2"}, // search domain
		} {
			if got := curl(t, tc.host); got != tc.want {
				t.Errorf("curl %s = %q, want %q", tc.host, got, tc.want)
			}
		}
		if got := udp(t, "peer-b1."+domB); got != "peer-b1" {
			t.Errorf("udp peer-b1 = %q", got)
		}
	})

	t.Run("ips", func(t *testing.T) {
		a1 := labtest.NodeIP(t, labtest.TailnetA, "peer-a1")
		if got := curl(t, a1.String()); got != "peer-a1" {
			t.Errorf("curl %s = %q, want peer-a1 (tailnet a keeps colliding IPs)", a1, got)
		}
		b1 := device(st, "b", "peer-b1")
		if b1 == nil || b1.Mapping == nil || !b1.Mapping.Auto {
			t.Fatalf("peer-b1 not auto-mapped: %+v", b1)
		}
		if got := curl(t, b1.RoutedIP.String()); got != "peer-b1" {
			t.Errorf("curl %s = %q, want peer-b1", b1.RoutedIP, got)
		}
		b3 := device(st, "b", "peer-b3")
		if b3.RoutedIP != b3.RealIP {
			t.Fatalf("peer-b3 should keep its real IP: %+v", b3)
		}
		if got := curl(t, b3.RealIP.String()); got != "peer-b3" {
			t.Errorf("curl %s = %q, want peer-b3", b3.RealIP, got)
		}
	})

	t.Run("manual", func(t *testing.T) {
		s := must(t)(tw.SetAutoMap(t.Context(), false))
		if b1 := device(s, "b", "peer-b1"); b1.RoutedIP.IsValid() {
			t.Errorf("peer-b1 still routed to %s without auto-mapping", b1.RoutedIP)
		}
		flushDNS(t)
		if out, err := labtest.Output("getent", "hosts", "peer-b1."+domB); err == nil {
			t.Errorf("peer-b1 resolves without route: %s", out)
		}
		unresolved := false
		for _, c := range s.Conflicts {
			unresolved = unresolved || (c.Kind == weave.ConflictIP && !c.Resolved)
		}
		if !unresolved {
			t.Errorf("no unresolved IP conflict listed: %+v", s.Conflicts)
		}

		s = must(t)(tw.Map(t.Context(), "b", device(s, "b", "peer-b1").PeerID, netip.MustParseAddr("198.18.0.42")))
		flushDNS(t)
		if got := curl(t, "peer-b1."+domB); got != "peer-b1" {
			t.Errorf("curl peer-b1 after manual mapping = %q", got)
		}
		if got := curl(t, "198.18.0.42"); got != "peer-b1" {
			t.Errorf("curl 198.18.0.42 = %q", got)
		}
		if got := curl(t, "peer-a1.a.lab"); got != "peer-a1" {
			t.Errorf("curl peer-a1 after mapping b's device away = %q", got)
		}
	})

	// The same through the command line, as a user would do it.
	t.Run("cli", func(t *testing.T) {
		cli := func(cmd string, args ...string) string { t.Helper(); return tw.cli(t, cmd, args...) }
		if out := cli("map", "b", "peer-b2", "198.18.0.43"); !strings.Contains(out, "198.18.0.43") {
			t.Errorf("map: %s", out)
		}
		flushDNS(t)
		if got := curl(t, "peer-b2."+domB); got != "peer-b2" {
			t.Errorf("curl peer-b2 after cli map = %q", got)
		}
		out := cli("status")
		for _, want := range []string{"auto-mapping off", "198.18.0.42", "198.18.0.43", "manual"} {
			if !strings.Contains(out, want) {
				t.Errorf("status lacks %q:\n%s", want, out)
			}
		}
		if out := cli("unmap", "b", "peer-b2"); !strings.Contains(out, "not routed") {
			t.Errorf("unmap: %s", out)
		}
		if out := cli("set", "-auto-map=true"); !strings.Contains(out, "auto-mapping on") {
			t.Errorf("set: %s", out)
		}
		s := waitState(t, tw, func(s daemon.State) bool { return device(s, "b", "peer-b2").RoutedIP.IsValid() })
		if b2 := device(s, "b", "peer-b2"); b2.Mapping == nil || !b2.Mapping.Auto {
			t.Errorf("peer-b2 not auto-mapped again: %+v", b2)
		}
	})
}

// TestLifecycle checks that tailnets stay logged in across daemon restarts, and that
// logout and remove clean up after themselves, as does stopping the daemon.
func TestLifecycle(t *testing.T) {
	labtest.RequireLab(t)
	dir := t.TempDir()
	tw := startTailweave(t, dir)
	tw.login(t, labtest.TailnetA)
	tw.stop(t)
	assertHostClean(t)

	tw = startTailweave(t, dir)
	s := waitState(t, tw, func(s daemon.State) bool { return device(s, "a", "peer-a1") != nil })
	if a := s.Tailnets[0]; a.State != "Running" || a.Self != "dut-a" {
		t.Fatalf("tailnet a after restart: %+v", a)
	}

	if out := tw.cli(t, "logout", "a"); !strings.Contains(out, "logged out") {
		t.Errorf("logout: %s", out)
	}
	s = must(t)(tw.State(t.Context()))
	if len(s.Tailnets) != 1 || s.Tailnets[0].State != "LoggedOut" || len(s.Devices) != 0 {
		t.Errorf("state after logout: %+v", s)
	}
	assertNoNetns(t)

	tw.login(t, labtest.TailnetA)
	tw.cli(t, "remove", "a")
	if s := must(t)(tw.State(t.Context())); len(s.Tailnets) != 0 {
		t.Errorf("tailnets after remove: %+v", s.Tailnets)
	}
	if _, err := labtest.Output("sudo", "-n", "test", "-e", filepath.Join(dir, "state", "tailnets", "a")); err == nil {
		t.Error("state of tailnet a still exists after remove")
	}
	assertNoNetns(t)
	tw.stop(t)
	assertHostClean(t)
}

func assertNoNetns(t *testing.T) {
	t.Helper()
	if out := labtest.Run(t, "ip", "netns", "list"); strings.Contains(out, "tailweave-") {
		t.Errorf("namespaces left:\n%s", out)
	}
}

// assertHostClean checks that a stopped tailweave left nothing behind.
func assertHostClean(t *testing.T) {
	t.Helper()
	assertNoNetns(t)
	if out := labtest.Run(t, "ip", "-br", "link"); strings.Contains(out, "twh") || strings.Contains(out, "tailweave0") {
		t.Errorf("interfaces left:\n%s", out)
	}
	if out := labtest.Run(t, "sudo", "-n", "nft", "list", "tables"); strings.Contains(out, "ip tailweave") {
		t.Errorf("nft tables left:\n%s", out)
	}
	if out, _ := labtest.Output("sudo", "-n", "iptables", "-S", "DOCKER-USER"); strings.Contains(out, "twh") {
		t.Errorf("DOCKER-USER rules left:\n%s", out)
	}
	if out := labtest.Run(t, "ip", "rule"); strings.Contains(out, "5250") {
		t.Errorf("ip rule left:\n%s", out)
	}
}

// tailweave is a daemon started by a test, with a client for its API.
type tailweave struct {
	*client.Client
	bin, dir string
	stop     func(t *testing.T)
}

// cli runs a tailweave command against the test's daemon and returns its output.
func (tw tailweave) cli(t *testing.T, cmd string, args ...string) string {
	t.Helper()
	args = append([]string{"-n", tw.bin, cmd, "-socket", tw.Socket}, args...)
	out, err := exec.Command("sudo", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("tailweave %s %v: %v\n%s", cmd, args[5:], err, out)
	}
	return string(out)
}

// login logs the daemon into a lab tailnet as dut-<tailnet> with the preauth key.
func (tw tailweave) login(t *testing.T, tailnet string) {
	t.Helper()
	out := tw.cli(t, "login", tailnet, "-login-server", labtest.LoginServer(tailnet), "-hostname", "dut-"+tailnet,
		"-auth-key", "file:"+labtest.AuthKeyFile(t, tw.dir, tailnet))
	if !strings.Contains(out, "logged in to "+tailnet+" as dut-"+tailnet) {
		t.Fatalf("login %s: %s", tailnet, out)
	}
}

func must(t *testing.T) func(daemon.State, error) daemon.State {
	return func(s daemon.State, err error) daemon.State {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
}

// startTailweave starts the daemon with its state in dir. It is stopped at the end of
// the test unless stopped before; then the test's nodes are removed from headscale.
func startTailweave(t *testing.T, dir string) tailweave {
	t.Helper()
	bin := filepath.Join(dir, "tailweave")
	labtest.Run(t, "go", "build", "-o", bin, "./cmd/tailweave")
	sock := filepath.Join(dir, "tailweave.sock")
	cfg, _ := json.Marshal(map[string]any{"stateDir": filepath.Join(dir, "state"), "socket": sock})
	conf := filepath.Join(dir, "config.json")
	if err := os.WriteFile(conf, cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// root's files would make the removal of the test's temp dir fail.
		_, _ = labtest.Output("sudo", "-n", "rm", "-rf", filepath.Join(dir, "state"))
		for _, tn := range []string{labtest.TailnetA, labtest.TailnetB} {
			labtest.ForgetNodes(t, tn, "dut-")
		}
	})

	var log bytes.Buffer
	cmd := exec.Command("sudo", "-n", bin, "run", "-config", conf)
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func(t *testing.T) {
		if stopped {
			return
		}
		stopped = true
		_ = cmd.Process.Signal(syscall.SIGTERM) // sudo relays it to tailweave
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("tailweave did not stop")
		}
		if t.Failed() {
			t.Logf("tailweave log:\n%s", log.String())
		}
	}
	t.Cleanup(func() { stop(t) })

	// The socket is root's only; open it up so the test's own client can use it.
	for i := 0; exec.Command("sudo", "-n", "chmod", "0666", sock).Run() != nil; i++ {
		if i == 30 {
			t.Fatalf("no API socket at %s:\n%s", sock, log.String())
		}
		time.Sleep(time.Second)
	}
	return tailweave{Client: client.New(sock), bin: bin, dir: dir, stop: stop}
}

func waitState(t *testing.T, tw tailweave, ok func(daemon.State) bool) daemon.State {
	t.Helper()
	var last string
	for range 30 {
		s, err := tw.State(t.Context())
		if err == nil && ok(s) {
			return s
		}
		last = fmt.Sprintf("%+v %v", s, err)
		time.Sleep(time.Second)
	}
	t.Fatalf("tailweave did not reach the expected state, last: %s", last)
	return daemon.State{}
}

func device(s daemon.State, tailnet, name string) *weave.Device {
	for i := range s.Devices {
		if d := &s.Devices[i]; d.Tailnet == tailnet && d.Name == name {
			return d
		}
	}
	return nil
}

func curl(t *testing.T, host string) string {
	t.Helper()
	out, _ := labtest.Output("curl", "-s", "-m5", "http://"+host+"/")
	return out
}

func udp(t *testing.T, host string) string {
	t.Helper()
	out, _ := labtest.Output("sh", "-c", "echo x | nc -u -w2 "+host+" 8")
	return strings.TrimSpace(out)
}

func flushDNS(t *testing.T) {
	t.Helper()
	labtest.Run(t, "sudo", "-n", "resolvectl", "flush-caches")
}
