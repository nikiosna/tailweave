// Package instance runs one tailscaled per tailnet, each in a network namespace of its
// own, and logs it in. It needs root and the tools ip, sysctl, tailscaled and tailscale.
//
// A tailnet's instance consists of the namespace "tailweave-<name>", a veth pair
// "twh<index>" (host) / "twn<index>" (namespace) with a /30 from the transit range, and a
// supervised tailscaled whose TUN is "ts0" inside the namespace. The tailscaled state
// directory outlives the instance, so a restarted instance is still logged in.
package instance

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Nikiosna/tailweave/internal/netcfg"
	"github.com/Nikiosna/tailweave/internal/tailnet"
)

// Tun is the name of tailscaled's TUN interface inside every namespace.
const Tun = "ts0"

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidName checks a tailnet name; it ends up in namespace and file names.
func ValidName(name string) error {
	if !validName.MatchString(name) {
		return fmt.Errorf("invalid tailnet name %q: use 1-32 of a-z, 0-9 and -, starting with a letter or digit", name)
	}
	return nil
}

// Addrs returns the host and namespace addresses of the index-th /30 in transit.
func Addrs(transit netip.Prefix, index int) (host, ns netip.Addr, err error) {
	if !transit.Addr().Is4() || transit.Bits() > 30 {
		return host, ns, fmt.Errorf("transit range %s must be IPv4 and hold at least one /30", transit)
	}
	if index < 0 || index >= 1<<(30-transit.Bits()) {
		return host, ns, fmt.Errorf("transit range %s has no room for instance %d", transit, index)
	}
	b := transit.Masked().Addr().As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	v += uint32(index) * 4
	at := func(off uint32) netip.Addr {
		x := v + off
		return netip.AddrFrom4([4]byte{byte(x >> 24), byte(x >> 16), byte(x >> 8), byte(x)})
	}
	return at(1), at(2), nil
}

// Manager knows where instances live.
type Manager struct {
	Transit  netip.Prefix
	StateDir string // tailscaled state per tailnet in <StateDir>/<name>
	RunDir   string // LocalAPI sockets and short-lived files
}

// Instance is a running tailscaled in its namespace.
type Instance struct {
	Name    string
	Netns   string
	Socket  string     // tailscaled's LocalAPI socket
	HostIP  netip.Addr // host side of the veth pair
	Gateway netip.Addr // namespace side, where the host routes the tailnet's IPs to

	m      *Manager
	hostIf string
	nsIf   string
	stop   context.CancelFunc
	done   chan struct{} // closed when the supervisor has stopped tailscaled

	mu       sync.Mutex
	upCancel context.CancelFunc
	upErr    string
}

// Namespace describes the instance for netcfg.
func (i *Instance) Namespace() netcfg.Namespace {
	return netcfg.Namespace{Tailnet: i.Name, Netns: i.Netns, Gateway: i.Gateway,
		HostIP: i.HostIP, Veth: i.nsIf, Tun: Tun}
}

// Start creates the namespace, starts tailscaled in it and waits for its socket. Leftovers
// of an earlier run under the same name are removed first.
func (m *Manager) Start(name string, index int) (*Instance, error) {
	if err := ValidName(name); err != nil {
		return nil, err
	}
	host, ns, err := Addrs(m.Transit, index)
	if err != nil {
		return nil, err
	}
	i := &Instance{
		Name:    name,
		Netns:   "tailweave-" + name,
		Socket:  filepath.Join(m.RunDir, name+".sock"),
		HostIP:  host,
		Gateway: ns,
		m:       m,
		hostIf:  fmt.Sprintf("%s%d", netcfg.VethPrefix, index),
		nsIf:    fmt.Sprintf("twn%d", index),
		done:    make(chan struct{}),
	}
	i.cleanup()
	if err := i.setupNetns(); err != nil {
		i.cleanup()
		return nil, fmt.Errorf("namespace for %s: %w", name, err)
	}
	for _, dir := range []string{m.RunDir, i.stateDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			i.cleanup()
			return nil, err
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	i.stop = cancel
	go i.supervise(ctx)
	for range 100 {
		if fi, err := os.Stat(i.Socket); err == nil && fi.Mode()&os.ModeSocket != 0 {
			// Tailnets logged in by an older tailweave still have subnet routes off.
			if err := run("tailscale", "--socket="+i.Socket, "set", "--accept-routes=true", "--accept-dns=false"); err != nil {
				slog.Warn("cannot turn on subnet routes", "tailnet", name, "err", err)
			}
			return i, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	i.Stop()
	return nil, fmt.Errorf("tailscaled for %s did not start, see %s", name, i.logPath())
}

func (i *Instance) stateDir() string { return filepath.Join(i.m.StateDir, i.Name) }
func (i *Instance) logPath() string  { return filepath.Join(i.stateDir(), "tailscaled.log") }

func (i *Instance) setupNetns() error {
	nsIf := i.nsIf
	for _, args := range [][]string{
		{"netns", "add", i.Netns},
		{"link", "add", i.hostIf, "type", "veth", "peer", "name", nsIf, "netns", i.Netns},
		{"addr", "add", netip.PrefixFrom(i.HostIP, 30).String(), "dev", i.hostIf},
		{"link", "set", i.hostIf, "up"},
		{"-n", i.Netns, "addr", "add", netip.PrefixFrom(i.Gateway, 30).String(), "dev", nsIf},
		{"-n", i.Netns, "link", "set", nsIf, "up"},
		{"-n", i.Netns, "link", "set", "lo", "up"},
		{"-n", i.Netns, "route", "add", "default", "via", i.HostIP.String()},
		// Subnet routes go to tailscaled's table 52, which is consulted before main. This
		// keeps the veth's own network out of their reach; only the default route is left
		// to them.
		{"-n", i.Netns, "rule", "add", "pref", "5200", "lookup", "main", "suppress_prefixlength", "0"},
	} {
		if err := run("ip", args...); err != nil {
			return err
		}
	}
	if err := run("ip", "netns", "exec", i.Netns, "sysctl", "-qw", "net.ipv4.ip_forward=1"); err != nil {
		return err
	}
	// `ip netns exec` bind-mounts this over /etc/resolv.conf, so tailscaled can resolve
	// its control server without the host's stub resolver on 127.0.0.53.
	dir := filepath.Join("/etc/netns", i.Netns)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "resolv.conf"), []byte(upstreamResolvConf()), 0o644)
}

// upstreamResolvConf returns the host's real upstream name servers in resolv.conf format.
func upstreamResolvConf() string {
	for _, p := range []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"} {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var out []string
		for _, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if len(f) == 2 && f[0] == "nameserver" {
				if a, err := netip.ParseAddr(f[1]); err != nil || a.IsLoopback() {
					continue
				}
				out = append(out, line)
			}
		}
		if len(out) > 0 {
			return strings.Join(out, "\n") + "\n"
		}
	}
	return "nameserver 1.1.1.1\n"
}

// supervise runs tailscaled and restarts it when it exits, until ctx is done.
func (i *Instance) supervise(ctx context.Context) {
	defer close(i.done)
	backoff := time.Second
	for {
		started := time.Now()
		err := i.runTailscaled(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(started) > time.Minute {
			backoff = time.Second
		}
		slog.Warn("tailscaled exited, restarting", "tailnet", i.Name, "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 30*time.Second)
	}
}

func (i *Instance) runTailscaled(ctx context.Context) error {
	log, err := os.OpenFile(i.logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	// `ip netns exec` execs tailscaled, so the signal reaches it directly.
	cmd := exec.CommandContext(ctx, "ip", "netns", "exec", i.Netns, "tailscaled",
		"--tun="+Tun, "--statedir="+i.stateDir(), "--socket="+i.Socket)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 10 * time.Second
	return cmd.Run()
}

// Login is what `tailscale up` gets.
type Login = tailnet.Login

// Up starts logging in and returns at once. Without an auth key, tailscaled publishes a
// login URL in its status; the login completes when the user has followed it. A failed
// attempt is reported by LoginError. An earlier attempt still waiting is cancelled.
func (i *Instance) Up(l Login) error {
	args := []string{"--socket=" + i.Socket, "up", "--reset", "--accept-dns=false", "--accept-routes=true",
		"--login-server=" + l.LoginServer, "--hostname=" + l.Hostname}
	var keyFile string
	if l.AuthKey != "" {
		// Pass the key via a file so it does not show up in the process list.
		f, err := os.CreateTemp(i.m.RunDir, i.Name+"-authkey-*")
		if err != nil {
			return err
		}
		keyFile = f.Name()
		_, err = f.WriteString(l.AuthKey)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(keyFile)
			return err
		}
		args = append(args, "--auth-key=file:"+keyFile)
	}

	ctx, cancel := context.WithCancel(context.Background())
	i.mu.Lock()
	if i.upCancel != nil {
		i.upCancel()
	}
	i.upCancel, i.upErr = cancel, ""
	i.mu.Unlock()

	go func() {
		defer cancel()
		if keyFile != "" {
			defer os.Remove(keyFile)
		}
		out, err := exec.CommandContext(ctx, "tailscale", args...).CombinedOutput()
		if err == nil || ctx.Err() != nil {
			return
		}
		msg := strings.TrimSpace(string(out))
		slog.Warn("login failed", "tailnet", i.Name, "err", err, "output", msg)
		i.mu.Lock()
		i.upErr = lastLine(msg, err)
		i.mu.Unlock()
	}()
	return nil
}

// LoginError is why the last login attempt failed, or "".
func (i *Instance) LoginError() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.upErr
}

// Logout logs the instance out of its tailnet; tailscaled keeps running.
func (i *Instance) Logout() error {
	i.cancelUp()
	return run("tailscale", "--socket="+i.Socket, "logout")
}

func (i *Instance) cancelUp() {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.upCancel != nil {
		i.upCancel()
		i.upCancel = nil
	}
}

// Stop stops tailscaled and removes the namespace. The state directory stays.
func (i *Instance) Stop() {
	i.cancelUp()
	i.stop()
	<-i.done
	i.cleanup()
}

// Purge removes a tailnet's tailscaled state, so it has to log in again.
func (m *Manager) Purge(name string) error {
	if err := ValidName(name); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(m.StateDir, name))
}

// cleanup removes the namespace (and with it the veth pair) and a tailscaled left over
// from a crashed run. Errors are ignored: the parts may not exist.
func (i *Instance) cleanup() {
	_ = run("pkill", "-f", "^tailscaled .*--socket="+regexp.QuoteMeta(i.Socket)+"$")
	_ = run("ip", "netns", "del", i.Netns)
	_ = run("ip", "link", "del", i.hostIf)
	_ = os.RemoveAll(filepath.Join("/etc/netns", i.Netns))
	_ = os.Remove(i.Socket)
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func lastLine(s string, err error) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return err.Error()
	}
	if i := strings.LastIndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return s
}
