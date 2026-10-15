package preflight

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// Collect takes a snapshot of the host's routes, interfaces and processes. macOS has
// no policy routing rules.
func Collect() (Snapshot, error) {
	var s Snapshot
	out, err := output("netstat", "-rn", "-f", "inet")
	if err != nil {
		return s, err
	}
	s.Routes = parseNetstat(out)
	if out, err = output("ifconfig"); err != nil {
		return s, err
	}
	s.Interfaces = parseIfconfig(out)
	if out, err = output("ps", "-axco", "comm"); err != nil {
		return s, err
	}
	s.Processes = strings.Split(strings.TrimSpace(out), "\n")
	for i, p := range s.Processes {
		s.Processes[i] = strings.TrimSpace(p)
	}
	return s, nil
}

func output(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}
