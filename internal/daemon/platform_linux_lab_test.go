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
	if out := labtest.Run(t, "ip", "rule"); strings.Contains(out, "5250") {
		t.Errorf("ip rule left:\n%s", out)
	}
}
