//go:build lab && linux

package daemon_test

import (
	"strings"
	"testing"

	"github.com/Nikiosna/tailweave/internal/labtest"
)

func flushDNS(t *testing.T) {
	t.Helper()
	labtest.Run(t, "sudo", "-n", "resolvectl", "flush-caches")
}

// resolve looks name up through the system resolver.
func resolve(t *testing.T, name string) (string, bool) {
	t.Helper()
	out, err := labtest.Output("getent", "hosts", name)
	return out, err == nil
}

// resolvesAAAA reports whether the system resolver asks for AAAA records in domain.
func resolvesAAAA(t *testing.T, domain string) bool { return true }

func assertNoInstances(t *testing.T) {
	t.Helper()
	if out := labtest.Run(t, "ip", "netns", "list"); strings.Contains(out, "tailweave-") {
		t.Errorf("namespaces left:\n%s", out)
	}
}

// assertHostClean checks that a stopped tailweave left nothing behind.
func assertHostClean(t *testing.T) {
	t.Helper()
	assertNoInstances(t)
	if out := labtest.Run(t, "ip", "-br", "link"); strings.Contains(out, "twh") || strings.Contains(out, "tailweave0") {
		t.Errorf("interfaces left:\n%s", out)
	}
	if out := labtest.Run(t, "sudo", "-n", "nft", "list", "tables"); strings.Contains(out, "ip tailweave") {
		t.Errorf("nft tables left:\n%s", out)
	}
	if out, _ := labtest.Output("sudo", "-n", "iptables", "-S", "DOCKER-USER"); strings.Contains(out, "twh") {
		t.Errorf("DOCKER-USER rules left:\n%s", out)
	}
	for _, family := range []string{"-4", "-6"} {
		out := labtest.Run(t, "ip", family, "rule")
		for _, pref := range []string{"5180:", "5181:", "5182:", "5183:"} {
			if strings.Contains(out, pref) {
				t.Errorf("ip %s rule left:\n%s", family, out)
			}
		}
		for _, table := range []string{"5180", "5181"} {
			// A table that never had a route since boot does not exist; ip then fails.
			out, err := labtest.Output("ip", family, "route", "show", "table", table)
			if err != nil && strings.Contains(out, "FIB table does not exist") {
				continue
			}
			if err != nil || out != "" {
				t.Errorf("ip %s routes left in table %s:\n%s", family, table, out)
			}
		}
	}
}

// exitRouted reports whether tailweave sends this machine's internet traffic to an exit
// node.
func exitRouted(t *testing.T) bool {
	t.Helper()
	return strings.Contains(labtest.Run(t, "ip", "rule"), "lookup 5181")
}

// tailscaleRules returns tailscaled's ip rules (priorities 5210-5270).
func tailscaleRules(t *testing.T) string {
	t.Helper()
	var rules []string
	for _, l := range strings.Split(labtest.Run(t, "ip", "rule"), "\n") {
		if p, _, ok := strings.Cut(l, ":"); ok && p >= "5210" && p <= "5270" && len(p) == 4 {
			rules = append(rules, l)
		}
	}
	if len(rules) == 0 {
		t.Error("no ip rules of tailscaled")
	}
	return strings.Join(rules, "\n")
}
