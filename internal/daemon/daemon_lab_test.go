//go:build lab

package daemon_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Nikiosna/tailweave/internal/daemon"
	"github.com/Nikiosna/tailweave/internal/labtest"
	"github.com/Nikiosna/tailweave/internal/weave"
)

const api = "http://127.0.0.1:5250"

// TestWeave runs tailweave against the playground and checks from the host itself that
// both tailnets are reachable by name and IP, with and without auto-mapping.
func TestWeave(t *testing.T) {
	labtest.RequireLab(t)
	labtest.Playground(t)
	startTailweave(t)

	// b's MagicDNS domain is a.lab in the dns-suffix scenario.
	domB := "b.lab"
	if labtest.Scenario() == "dns-suffix" {
		domB = "a.lab"
	}
	st := waitState(t, func(s daemon.State) bool { return device(s, "b", "peer-b3") != nil })

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
		s := call(t, http.MethodPut, "/api/options", map[string]bool{"autoMap": false})
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

		s = call(t, http.MethodPost, "/api/mappings", daemon.MappingRequest{
			Tailnet: "b", PeerID: device(s, "b", "peer-b1").PeerID, IP: netip.MustParseAddr("198.18.0.42")})
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
}

func startTailweave(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "tailweave")
	labtest.Run(t, "go", "build", "-o", bin, "./cmd/tailweave")

	var cfg map[string]any
	b, err := os.ReadFile(filepath.Join(labtest.Root(), "lab", "tailweave.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		t.Fatal(err)
	}
	cfg["state"] = filepath.Join(dir, "state.json")
	b, _ = json.Marshal(cfg)
	conf := filepath.Join(dir, "config.json")
	if err := os.WriteFile(conf, b, 0o644); err != nil {
		t.Fatal(err)
	}

	var log bytes.Buffer
	cmd := exec.Command("sudo", "-n", bin, "run", "-config", conf)
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM) // sudo relays it to tailweave
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("tailweave did not stop")
		}
		if t.Failed() {
			t.Logf("tailweave log:\n%s", log.String())
		}
	})
}

func call(t *testing.T, method, path string, body any) daemon.State {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, api+path, rd)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("%s %s: %s: %s", method, path, resp.Status, msg)
	}
	var s daemon.State
	if err := json.NewDecoder(resp.Body).Decode(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

func waitState(t *testing.T, ok func(daemon.State) bool) daemon.State {
	t.Helper()
	var last string
	for range 30 {
		if resp, err := http.Get(api + "/api/state"); err == nil {
			var s daemon.State
			err = json.NewDecoder(resp.Body).Decode(&s)
			resp.Body.Close()
			if err == nil && ok(s) {
				return s
			}
			last = fmt.Sprintf("%+v", s)
		}
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
