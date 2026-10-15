//go:build lab && darwin

package daemon_test

import (
	"strings"
	"testing"

	"github.com/Nikiosna/tailweave/internal/labtest"
)

func flushDNS(t *testing.T) {
	t.Helper()
	labtest.Run(t, "sudo", "-n", "dscacheutil", "-flushcache")
	_, _ = labtest.Output("sudo", "-n", "killall", "-HUP", "mDNSResponder")
}

// resolve looks name up through the system resolver.
func resolve(t *testing.T, name string) (string, bool) {
	t.Helper()
	out, _ := labtest.Output("dscacheutil", "-q", "host", "-a", "name", name)
	return out, strings.Contains(out, "ip_address:")
}

// assertNoInstances has nothing to check: the tsnet backend runs its engines in-process.
func assertNoInstances(t *testing.T) {}

// assertHostClean checks that a stopped tailweave left nothing behind.
func assertHostClean(t *testing.T) {
	t.Helper()
	if out := labtest.Run(t, "ifconfig", "lo0"); strings.Contains(out, "100.100.100.100") {
		t.Errorf("DNS address left on lo0:\n%s", out)
	}
	if out, _ := labtest.Output("sh", "-c", "echo 'show State:/Network/Service/tailweave/DNS' | scutil"); !strings.Contains(out, "No such key") {
		t.Errorf("DNS settings left:\n%s", out)
	}
	if out := labtest.Run(t, "netstat", "-rn", "-f", "inet"); strings.Contains(out, "100.64/10") || strings.Contains(out, "198.18/15") {
		t.Errorf("routes left:\n%s", out)
	}
}

// tailscaleRules has nothing to return: macOS has no policy routing rules.
func tailscaleRules(t *testing.T) string { return "" }
