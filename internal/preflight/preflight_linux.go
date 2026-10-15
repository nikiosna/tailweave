package preflight

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Collect takes a snapshot of the host's network namespace. Network namespaces of
// tailweave's tailnets (netns backend) are separate and not part of it.
func Collect() (Snapshot, error) {
	var s Snapshot
	b, err := ip("route", "show", "table", "all")
	if err != nil {
		return s, err
	}
	if s.Routes, err = parseIPRoutes(b); err != nil {
		return s, err
	}
	if b, err = ip("rule"); err != nil {
		return s, err
	}
	if s.Rules, err = parseIPRules(b); err != nil {
		return s, err
	}
	if b, err = ip("addr"); err != nil {
		return s, err
	}
	if s.Interfaces, err = parseIPAddrs(b); err != nil {
		return s, err
	}
	comms, _ := filepath.Glob("/proc/[0-9]*/comm")
	for _, c := range comms {
		if b, err := os.ReadFile(c); err == nil {
			s.Processes = append(s.Processes, strings.TrimSpace(string(b)))
		}
	}
	return s, nil
}

func ip(args ...string) ([]byte, error) {
	cmd := exec.Command("ip", append([]string{"-4", "-j"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ip %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}
