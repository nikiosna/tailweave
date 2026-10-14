// Package labtest drives the lab in lab/ from Go integration tests.
//
// Tests using it carry the build tag "lab" and expect a running lab
// (make lab-up) on a Linux host with Docker and sudo.
package labtest

import (
	"bytes"
	"encoding/json"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Tailnets in the lab, named after their headscale instance.
const (
	TailnetA = "a"
	TailnetB = "b"
)

// Node is a node as reported by headscale.
type Node struct {
	ID   uint64   `json:"id"`
	Name string   `json:"given_name"`
	IPs  []string `json:"ip_addresses"`
}

// IPv4 returns the node's tailnet IPv4 address.
func (n Node) IPv4() netip.Addr {
	for _, s := range n.IPs {
		if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
			return a
		}
	}
	return netip.Addr{}
}

// Root returns the repository root.
func Root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

// Run runs a command and fails the test if it does not succeed. It returns trimmed stdout.
func Run(t testing.TB, name string, args ...string) string {
	t.Helper()
	out, err := Output(name, args...)
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", name, strings.Join(args, " "), err, out)
	}
	return out
}

// Output runs a command and returns trimmed stdout, or stdout+stderr on error.
func Output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = Root()
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		return strings.TrimSpace(stdout.String() + stderr.String()), err
	}
	return strings.TrimSpace(stdout.String()), nil
}

// Nodes lists all nodes of a lab tailnet.
func Nodes(t testing.TB, tailnet string) []Node {
	t.Helper()
	out := Run(t, "docker", "exec", "headscale-"+tailnet, "headscale", "nodes", "list", "-o", "json")
	var nodes []Node
	if err := json.Unmarshal([]byte(out), &nodes); err != nil {
		t.Fatalf("parse nodes of %s: %v", tailnet, err)
	}
	return nodes
}

// NodeIP returns the IPv4 address of the named node in a tailnet.
func NodeIP(t testing.TB, tailnet, name string) netip.Addr {
	t.Helper()
	for _, n := range Nodes(t, tailnet) {
		if n.Name == name {
			return n.IPv4()
		}
	}
	t.Fatalf("node %s not found in tailnet %s", name, tailnet)
	return netip.Addr{}
}

// RequireLab skips the test if the lab is not running.
func RequireLab(t testing.TB) {
	t.Helper()
	for _, tn := range []string{TailnetA, TailnetB} {
		if _, err := Output("docker", "exec", "headscale-"+tn, "headscale", "users", "list"); err != nil {
			t.Skipf("lab not running (headscale-%s unavailable), run `make lab-up`", tn)
		}
	}
}

// Scenario returns the scenario the lab was started with (make lab-up SCENARIO=...).
func Scenario() string {
	b, err := os.ReadFile(filepath.Join(Root(), "lab", ".state", "scenario.env"))
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "SCENARIO="); ok {
			return v
		}
	}
	return ""
}
