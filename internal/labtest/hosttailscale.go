package labtest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Where HostTailscale keeps its state and control socket, apart from a tailscaled the
// machine may run as a service.
const (
	hostTSState  = "/var/lib/tailweave-lab/host"
	hostTSSocket = "/var/run/tailweave-lab/host/tailscaled.sock"
	hostTSPort   = "41700"
	hostTSName   = "dut-host"
)

// HostTailscale is the official Tailscale client running in the host's own network
// namespace, as a user would run it next to tailweave.
type HostTailscale struct {
	cli     string
	log     string
	stopped bool
	stop    func()
}

// StartHostTailscale starts tailscaled on the host and logs it into a lab tailnet as
// dut-host with the preauth key. It is stopped at the end of the test unless stopped
// before; then its node is removed from headscale.
//
// The binaries come from $TAILSCALED_BIN and $TAILSCALE_BIN, else from next to
// $TAILWEAVE_BIN (make mac-build puts them there), else from PATH.
func StartHostTailscale(t testing.TB, tailnet string) *HostTailscale {
	t.Helper()
	daemon, cli := tailscaleBin("TAILSCALED_BIN", "tailscaled"), tailscaleBin("TAILSCALE_BIN", "tailscale")
	dir := t.TempDir()
	h := &HostTailscale{cli: cli, log: filepath.Join(dir, "tailscaled.log")}

	ForgetNodes(t, tailnet, hostTSName)
	Run(t, "sudo", "-n", "rm", "-rf", hostTSState)
	Run(t, "sudo", "-n", "mkdir", "-p", hostTSState, filepath.Dir(hostTSSocket))
	log, err := os.Create(h.log)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sudo", "-n", daemon, "--statedir="+hostTSState, "--socket="+hostTSSocket,
		"--port="+hostTSPort, "--no-logs-no-support")
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = cmd.Wait(); log.Close(); close(done) }()
	h.stop = func() {
		_ = cmd.Process.Signal(syscall.SIGTERM) // sudo relays it to tailscaled
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Errorf("tailscaled did not stop")
			_, _ = Output("sudo", "-n", "pkill", "-KILL", "-f", "tailscaled --statedir="+hostTSState)
		}
	}
	t.Cleanup(func() {
		h.Stop(t)
		_, _ = Output("sudo", "-n", "rm", "-rf", hostTSState)
		ForgetNodes(t, tailnet, hostTSName)
	})

	for i := 0; exec.Command("sudo", "-n", "test", "-S", hostTSSocket).Run() != nil; i++ {
		if i == 30 {
			t.Fatalf("tailscaled did not open %s:\n%s", hostTSSocket, h.Log())
		}
		time.Sleep(time.Second)
	}
	if out, err := h.Output("up", "--login-server="+LoginServer(tailnet),
		"--auth-key=file:"+AuthKeyFile(t, dir, tailnet), "--hostname="+hostTSName,
		"--accept-dns=false", "--timeout=60s"); err != nil {
		t.Fatalf("tailscale up: %v\n%s\ntailscaled log:\n%s", err, out, h.Log())
	}
	return h
}

func tailscaleBin(env, name string) string {
	if b := os.Getenv(env); b != "" {
		return b
	}
	if tw := os.Getenv("TAILWEAVE_BIN"); tw != "" {
		if b := filepath.Join(filepath.Dir(tw), name); fileExists(b) {
			return b
		}
	}
	if b, err := exec.LookPath(name); err == nil {
		return b
	}
	return name
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// Output runs the tailscale CLI against the host's tailscaled.
func (h *HostTailscale) Output(args ...string) (string, error) {
	return Output("sudo", append([]string{"-n", h.cli, "--socket=" + hostTSSocket}, args...)...)
}

// Log returns what tailscaled logged so far.
func (h *HostTailscale) Log() string {
	b, _ := os.ReadFile(h.log)
	return strings.TrimSpace(string(b))
}

// Stop stops tailscaled; it cleans up its routes and rules on the way out.
func (h *HostTailscale) Stop(t testing.TB) {
	t.Helper()
	if h.stopped {
		return
	}
	h.stopped = true
	h.stop()
}
