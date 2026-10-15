//go:build lab

package daemon_test

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
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
			if got := waitCurl(t, tc.host, tc.want); got != tc.want {
				t.Errorf("curl %s = %q, want %q", tc.host, got, tc.want)
			}
		}
		if got := udp(t, "peer-b1."+domB); got != "peer-b1" {
			t.Errorf("udp peer-b1 = %q", got)
		}
	})

	t.Run("ips", func(t *testing.T) {
		a1 := labtest.NodeIP(t, labtest.TailnetA, "peer-a1")
		if got := waitCurl(t, a1.String(), "peer-a1"); got != "peer-a1" {
			t.Errorf("curl %s = %q, want peer-a1 (tailnet a keeps colliding IPs)", a1, got)
		}
		b1 := device(st, "b", "peer-b1")
		if b1 == nil || b1.Mapping == nil || !b1.Mapping.Auto {
			t.Fatalf("peer-b1 not auto-mapped: %+v", b1)
		}
		if got := waitCurl(t, b1.RoutedIP.String(), "peer-b1"); got != "peer-b1" {
			t.Errorf("curl %s = %q, want peer-b1", b1.RoutedIP, got)
		}
		b3 := device(st, "b", "peer-b3")
		if b3.RoutedIP != b3.RealIP {
			t.Fatalf("peer-b3 should keep its real IP: %+v", b3)
		}
		if got := waitCurl(t, b3.RealIP.String(), "peer-b3"); got != "peer-b3" {
			t.Errorf("curl %s = %q, want peer-b3", b3.RealIP, got)
		}
	})

	t.Run("manual", func(t *testing.T) {
		s := must(t)(tw.SetAutoMap(t.Context(), false))
		if b1 := device(s, "b", "peer-b1"); b1.RoutedIP.IsValid() {
			t.Errorf("peer-b1 still routed to %s without auto-mapping", b1.RoutedIP)
		}
		flushDNS(t)
		if out, ok := resolve(t, "peer-b1."+domB); ok {
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

// TestInbound checks that peers reach services on the host, like with Tailscale: a peer
// arrives from its own IP, or from the IP it is routed at when it collides with a peer of
// another tailnet. The peers connect back on request of the lab's probe service, which
// the host reaches through tailweave too, so the test needs no Docker.
func TestInbound(t *testing.T) {
	labtest.RequireLab(t)
	tw := startTailweave(t, t.TempDir())
	tw.login(t, labtest.TailnetA)
	tw.login(t, labtest.TailnetB)
	st := waitState(t, tw, func(s daemon.State) bool {
		b1 := device(s, "b", "peer-b1")
		return b1 != nil && b1.RoutedIP.IsValid() && device(s, "a", "peer-a1") != nil &&
			s.Tailnets[0].IP.IsValid() && s.Tailnets[1].IP.IsValid()
	})
	tcpPort, udpPort := remoteAddrServers(t)

	for i, name := range []string{"peer-a1", "peer-b1"} {
		tn := st.Tailnets[i]
		d := device(st, tn.Name, name)
		t.Run(name, func(t *testing.T) {
			want := d.RoutedIP.String() // b's peer-b1 collides with peer-a1: 198.18.x
			if got := waitProbe(t, d.RoutedIP, "tcp", tn.IP, tcpPort, want); got != want {
				t.Errorf("%s -> tcp %s:%d: host saw %q, want %q", name, tn.IP, tcpPort, got, want)
			}
			if got := probe(t, d.RoutedIP, "udp", tn.IP, udpPort); got != want {
				t.Errorf("%s -> udp %s:%d: host saw %q, want %q", name, tn.IP, udpPort, got, want)
			}
			if got := probe(t, d.RoutedIP, "ping", tn.IP, 0); got != "ok" {
				t.Errorf("%s -> ping %s: %q", name, tn.IP, got)
			}
		})
	}
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
	assertNoInstances(t)

	tw.login(t, labtest.TailnetA)
	tw.cli(t, "remove", "a")
	if s := must(t)(tw.State(t.Context())); len(s.Tailnets) != 0 {
		t.Errorf("tailnets after remove: %+v", s.Tailnets)
	}
	if _, err := labtest.Output("sudo", "-n", "test", "-e", filepath.Join(dir, "state", "tailnets", "a")); err == nil {
		t.Error("state of tailnet a still exists after remove")
	}
	assertNoInstances(t)
	tw.stop(t)
	assertHostClean(t)
}

// TestLoginSSO logs in via the web link: `tailweave login` prints headscale's URL, a
// headless browser signs in at the lab's Dex and confirms, and the login completes.
func TestLoginSSO(t *testing.T) {
	labtest.RequireLab(t)
	tw := startTailweave(t, t.TempDir())

	cmd := exec.Command("sudo", "-n", tw.bin, "login", "a", "-socket", tw.Socket,
		"-login-server", labtest.LoginServer(labtest.TailnetA), "-hostname", "dut-a", "-timeout", "90s")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = cmd.Stdout
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	url := ""
	sc := bufio.NewScanner(stdout)
	for url == "" && sc.Scan() {
		out.WriteString(sc.Text() + "\n")
		if u, ok := strings.CutPrefix(strings.TrimSpace(sc.Text()), "http"); ok {
			url = "http" + u
		}
	}
	if url == "" {
		_ = cmd.Wait()
		t.Fatalf("no login URL from tailweave login:\n%s", out.String())
	}
	if status := must(t)(tw.State(t.Context())).Tailnets[0]; status.State != "NeedsLogin" || status.AuthURL != url {
		t.Errorf("pending login in state: %+v", status)
	}

	labtest.SSOLogin(t, url)
	rest, _ := io.ReadAll(stdout)
	out.Write(rest)
	if err := cmd.Wait(); err != nil || !strings.Contains(out.String(), "logged in to a as dut-a") {
		t.Fatalf("tailweave login: %v\n%s", err, out.String())
	}
	waitState(t, tw, func(s daemon.State) bool { return device(s, "a", "peer-a1") != nil })
	if got := waitCurl(t, "peer-a1.a.lab", "peer-a1"); got != "peer-a1" {
		t.Errorf("curl peer-a1 after SSO login = %q", got)
	}
}

// TestForeignTailscale runs the official Tailscale client on the host: tailweave must
// refuse to start next to it, without breaking it, and start once it is gone.
func TestForeignTailscale(t *testing.T) {
	labtest.RequireLab(t)
	ts := labtest.StartHostTailscale(t, labtest.TailnetA)
	before := tailscaleRules(t)

	out := runTailweaveExpectFail(t, t.TempDir())
	t.Logf("tailweave refused to start:\n%s", out)
	if !strings.Contains(out, "Tailscale is running") {
		t.Errorf("tailweave's message does not mention Tailscale:\n%s", out)
	}

	a1 := labtest.NodeIP(t, labtest.TailnetA, "peer-a1")
	if out, err := ts.Output("ping", "--c=5", "--until-direct=false", a1.String()); err != nil {
		t.Errorf("tailscale ping %s after tailweave gave up: %v\n%s", a1, err, out)
	}
	if got := curl(t, a1.String()); got != "peer-a1" {
		t.Errorf("curl %s through the host's Tailscale = %q, want peer-a1", a1, got)
	}
	if after := tailscaleRules(t); after != before {
		t.Errorf("Tailscale's ip rules changed:\nbefore:\n%s\nafter:\n%s", before, after)
	}

	ts.Stop(t)
	tw := startTailweave(t, t.TempDir())
	tw.login(t, labtest.TailnetA)
	waitState(t, tw, func(s daemon.State) bool { return device(s, "a", "peer-a1") != nil })
	flushDNS(t) // macOS may have cached a negative answer while Tailscale was up
	if got := waitCurl(t, "peer-a1.a.lab", "peer-a1"); got != "peer-a1" {
		t.Errorf("curl peer-a1 through tailweave after Tailscale stopped = %q", got)
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
//
// $TAILWEAVE_BACKEND picks the backend (default: the platform's), $TAILWEAVE_BIN a
// prebuilt binary (default: built from the repository, which needs Go).
func startTailweave(t *testing.T, dir string) tailweave {
	t.Helper()
	bin, confPath, sock := setupTailweave(t, dir)
	t.Cleanup(func() {
		// root's files would make the removal of the test's temp dir fail.
		_, _ = labtest.Output("sudo", "-n", "rm", "-rf", filepath.Join(dir, "state"))
		for _, tn := range []string{labtest.TailnetA, labtest.TailnetB} {
			labtest.ForgetNodes(t, tn, "dut-")
		}
	})

	var log bytes.Buffer
	cmd := exec.Command("sudo", "-n", bin, "run", "-config", confPath)
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

// setupTailweave builds tailweave unless $TAILWEAVE_BIN names a binary and writes a
// config with the state in dir. It returns the binary, the config and the API socket.
func setupTailweave(t *testing.T, dir string) (bin, confPath, sock string) {
	t.Helper()
	bin = os.Getenv("TAILWEAVE_BIN")
	if bin == "" {
		bin = filepath.Join(dir, "tailweave")
		labtest.Run(t, "go", "build", "-o", bin, "./cmd/tailweave")
	}
	sock = filepath.Join(dir, "tailweave.sock")
	conf := map[string]any{"stateDir": filepath.Join(dir, "state"), "socket": sock}
	if b := os.Getenv("TAILWEAVE_BACKEND"); b != "" {
		conf["backend"] = b
	}
	cfg, _ := json.Marshal(conf)
	confPath = filepath.Join(dir, "config.json")
	if err := os.WriteFile(confPath, cfg, 0o644); err != nil {
		t.Fatal(err)
	}
	return bin, confPath, sock
}

// runTailweaveExpectFail runs the daemon with its state in dir, expects it to give up
// within 30 seconds and returns what it printed.
func runTailweaveExpectFail(t *testing.T, dir string) string {
	t.Helper()
	bin, confPath, _ := setupTailweave(t, dir)
	t.Cleanup(func() { _, _ = labtest.Output("sudo", "-n", "rm", "-rf", filepath.Join(dir, "state")) })
	var log bytes.Buffer
	cmd := exec.Command("sudo", "-n", bin, "run", "-config", confPath)
	cmd.Stdout, cmd.Stderr = &log, &log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatalf("tailweave exited successfully, want a failure:\n%s", log.String())
		}
	case <-time.After(30 * time.Second):
		_ = cmd.Process.Signal(syscall.SIGTERM)
		<-done
		t.Fatalf("tailweave still running after 30s, want a failure:\n%s", log.String())
	}
	return log.String()
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

// waitCurl retries curl until it returns want, for up to 20 seconds, and returns the
// last answer. A peer may take a moment to learn about a node that just logged in.
func waitCurl(t *testing.T, host, want string) string {
	t.Helper()
	var got string
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(time.Second) {
		if got = curl(t, host); got == want {
			break
		}
	}
	return got
}

// remoteAddrServers starts a TCP (HTTP) and a UDP service on all of the host's addresses
// that answer with the client's IP, and returns their ports.
func remoteAddrServers(t *testing.T) (tcpPort, udpPort int) {
	t.Helper()
	ln, err := net.Listen("tcp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		_, _ = io.WriteString(w, host)
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	pc, err := net.ListenPacket("udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			if n == 0 {
				continue // socat sends an empty datagram at the end of its input
			}
			_, _ = pc.WriteTo([]byte(addr.(*net.UDPAddr).IP.String()), addr)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, pc.LocalAddr().(*net.UDPAddr).Port
}

// probe asks the lab probe of the peer the host reaches at peer to connect back to ip:port
// ("tcp", "udp") or to ping ip, and returns what it got.
func probe(t *testing.T, peer netip.Addr, kind string, ip netip.Addr, port int) string {
	t.Helper()
	c, err := net.DialTimeout("tcp", net.JoinHostPort(peer.String(), "9"), 5*time.Second)
	if err != nil {
		t.Logf("probe %s: %v", peer, err)
		return ""
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := fmt.Fprintf(c, "%s %s %d\n", kind, ip, port); err != nil {
		t.Logf("probe %s: %v", peer, err)
		return ""
	}
	out, _ := io.ReadAll(c)
	return strings.TrimSpace(string(out))
}

// waitProbe retries probe until it returns want, for up to 20 seconds, like waitCurl.
func waitProbe(t *testing.T, peer netip.Addr, kind string, ip netip.Addr, port int, want string) string {
	t.Helper()
	var got string
	for end := time.Now().Add(20 * time.Second); time.Now().Before(end); time.Sleep(time.Second) {
		if got = probe(t, peer, kind, ip, port); got == want {
			break
		}
	}
	return got
}

func udp(t *testing.T, host string) string {
	t.Helper()
	out, _ := labtest.Output("sh", "-c", "echo x | nc -u -w2 "+host+" 8")
	return strings.TrimSpace(out)
}
