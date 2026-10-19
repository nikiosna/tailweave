// Package labtest drives the lab in lab/ from Go integration tests.
//
// Tests using it carry the build tag "lab" and expect a running lab (make lab-up) and
// sudo. They talk to the headscales over their REST API with the keys bootstrap.sh left
// in lab/.state/keys.env, so they also run on a machine without Docker, such as the
// macOS VM, as long as it reaches the lab network.
package labtest

import (
	"bytes"
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

// Node is a node as reported by headscale's REST API.
type Node struct {
	ID   string   `json:"id"`
	Name string   `json:"givenName"`
	IPs  []string `json:"ipAddresses"`
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

// RequireScenario skips the test unless the lab runs the given scenario, alone or as
// part of "all".
func RequireScenario(t testing.TB, scenario string) {
	t.Helper()
	if s := Scenario(); s != scenario && s != "all" {
		t.Skipf("needs the lab scenario %s (now %q), run `make lab-up SCENARIO=%s`", scenario, s, scenario)
	}
}

// LoginServer returns the control URL of a lab tailnet.
func LoginServer(tailnet string) string {
	return map[string]string{TailnetA: "http://172.30.0.10:8080", TailnetB: "http://172.30.0.11:8080"}[tailnet]
}
