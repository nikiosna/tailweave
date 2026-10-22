// Package engine runs tailweave's tailnets in-process: one Tailscale engine (tsnet) per
// tailnet, all attached to a userspace router behind a single TUN device on the host.
// It needs no network namespaces, so it is how tailweave works on macOS.
//
// A tailnet's state (keys, node identity) lives in <StateDir>/<name> and outlives the
// engine, so a restarted daemon is still logged in.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/Nikiosna/tailweave/internal/hostnet"
	"github.com/Nikiosna/tailweave/internal/router"
	"github.com/Nikiosna/tailweave/internal/tailnet"
	"github.com/Nikiosna/tailweave/internal/weave"
	"github.com/tailscale/wireguard-go/tun"
	"tailscale.com/envknob"
	"tailscale.com/ipn"
	"tailscale.com/logtail"
	"tailscale.com/tsnet"
)

// Backend owns the host TUN, the router and the tailnets' engines.
type Backend struct {
	StateDir string         // per tailnet state in <StateDir>/<name>
	HostIP   netip.Addr     // address of the TUN on the host
	HostIP6  netip.Addr     // its IPv6 address, for 4via6 routes; zero: no IPv6
	DNS      netip.Addr     // address of the DNS service
	Routes   []netip.Prefix // ranges always routed into the TUN; subnet routes are added

	dev  tun.Device
	net  *hostnet.Host
	r    *router.Router
	done chan error

	mu    sync.Mutex
	insts map[string]*Instance
}

// Cleanup removes what an earlier run left on the host, if it did not exit cleanly.
func (b *Backend) Cleanup() { hostnet.Cleanup(b.DNS) }

// Setup creates the TUN, configures the host and starts the router.
func (b *Backend) Setup() error {
	// Tailscale's engines would upload their logs to Tailscale Inc. otherwise.
	envknob.SetNoLogsNoSupport()
	logtail.Disable()

	dev, err := tun.CreateTUN(hostnet.TunName, router.MTU)
	if err != nil {
		return fmt.Errorf("create TUN: %w", err)
	}
	name, err := dev.Name()
	if err != nil {
		dev.Close()
		return err
	}
	b.dev = dev
	b.net = &hostnet.Host{Tun: name, HostIP: b.HostIP, HostIP6: b.HostIP6, DNS: b.DNS, Routes: b.Routes}
	if err := b.net.Setup(); err != nil {
		return err
	}
	b.r = router.New(dev, b.HostIP, b.HostIP6)
	b.done = make(chan error, 1)
	go func() { b.done <- b.r.Run() }()
	b.insts = map[string]*Instance{}
	slog.Info("router up", "tun", name, "address", b.HostIP)
	return nil
}

// Teardown stops the router and undoes the host configuration. Engines must have been
// stopped before.
func (b *Backend) Teardown() {
	if b.net != nil {
		b.net.Teardown()
		b.net = nil
	}
	if b.dev != nil {
		b.dev.Close()
		if b.done != nil {
			if err := <-b.done; err != nil {
				slog.Warn("router", "err", err)
			}
			b.done = nil
		}
		b.dev = nil
	}
}

// Apply routes by the plan and registers its domains with the system resolver.
func (b *Backend) Apply(p weave.Plan) error {
	b.mu.Lock()
	for name, i := range b.insts {
		v4, v6 := i.selfIPs()
		b.r.SetSelf(name, v4, v6)
	}
	b.mu.Unlock()
	b.r.SetPlan(p)
	// Subnet routes outside the ranges routed anyway, which hold the pool and 4via6.
	var extra []netip.Prefix
	for s := range p.Subnets {
		if !slices.ContainsFunc(b.Routes, func(r netip.Prefix) bool { return r.Contains(s.Addr()) && r.Bits() <= s.Bits() }) {
			extra = append(extra, s)
		}
	}
	slices.SortFunc(extra, func(a, b netip.Prefix) int { return a.Addr().Compare(b.Addr()) })
	if err := b.net.SetRoutes(extra); err != nil {
		return err
	}
	return b.net.SetDomains(p.Domains)
}

// Purge removes a tailnet's state, so it has to log in again.
func (b *Backend) Purge(name string) error {
	return os.RemoveAll(filepath.Join(b.StateDir, name))
}

// Instance is a tailnet's engine. Its server only exists once there is something to run:
// a stored login, or a login in progress.
type Instance struct {
	name string
	dir  string
	b    *Backend
	log  *os.File

	mu       sync.Mutex
	login    tailnet.Login // server and host name, without the key
	srv      *tsnet.Server
	upCancel context.CancelFunc
	upErr    string
}

// Start prepares a tailnet's engine. If the tailnet logged in before, its engine starts
// right away with the stored state.
func (b *Backend) Start(name string, l tailnet.Login) (*Instance, error) {
	dir := filepath.Join(b.StateDir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	log, err := os.OpenFile(filepath.Join(dir, "tailscaled.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	l.AuthKey = ""
	i := &Instance{name: name, dir: dir, b: b, log: log, login: l}
	if _, err := os.Stat(filepath.Join(dir, "tailscaled.state")); err == nil {
		if err := i.Up(l); err != nil {
			log.Close()
			return nil, err
		}
	}
	b.mu.Lock()
	b.insts[name] = i
	b.mu.Unlock()
	return i, nil
}

// Up (re)starts the engine and logs in. Without an auth key and without a stored login,
// the engine publishes a login URL in its status; the login completes when the user has
// followed it. A failed attempt is reported by LoginError.
func (i *Instance) Up(l tailnet.Login) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.stopLocked()
	i.login = tailnet.Login{LoginServer: l.LoginServer, Hostname: l.Hostname}
	i.upErr = ""

	srv := &tsnet.Server{
		Dir:        i.dir,
		Hostname:   l.Hostname,
		ControlURL: l.LoginServer,
		AuthKey:    l.AuthKey,
		Tun:        i.b.r.AddTailnet(i.name),
		Logf:       i.logf,
		UserLogf:   i.logf,
	}
	if err := srv.Start(); err != nil {
		i.b.r.RemoveTailnet(i.name)
		return fmt.Errorf("start engine for %s: %w", i.name, err)
	}
	lc, err := srv.LocalClient()
	if err == nil {
		// Subnet routes reach the engine, so it sends to them; which of them the host
		// reaches, and DNS, are tailweave's business.
		_, err = lc.EditPrefs(context.Background(), &ipn.MaskedPrefs{
			RouteAllSet: true, CorpDNSSet: true,
			Prefs: ipn.Prefs{RouteAll: true, CorpDNS: false},
		})
	}
	if err != nil {
		srv.Close()
		i.b.r.RemoveTailnet(i.name)
		return fmt.Errorf("configure engine for %s: %w", i.name, err)
	}
	i.srv = srv

	ctx, cancel := context.WithCancel(context.Background())
	i.upCancel = cancel
	go func() {
		defer cancel()
		_, err := srv.Up(ctx)
		if err == nil || ctx.Err() != nil {
			return
		}
		slog.Warn("login failed", "tailnet", i.name, "err", err)
		i.mu.Lock()
		if i.srv == srv {
			i.upErr = err.Error()
		}
		i.mu.Unlock()
	}()
	return nil
}

func (i *Instance) logf(format string, args ...any) {
	fmt.Fprintf(i.log, time.Now().Format("2006/01/02 15:04:05 ")+format+"\n", args...)
}

// Status returns the engine's state and, while it is Running, its tailnet.
func (i *Instance) Status(ctx context.Context) (tailnet.Status, error) {
	i.mu.Lock()
	srv := i.srv
	i.mu.Unlock()
	if srv == nil {
		return tailnet.Status{Backend: "NoState"}, nil
	}
	lc, err := srv.LocalClient()
	if err != nil {
		return tailnet.Status{}, err
	}
	st, err := lc.Status(ctx)
	if err != nil {
		return tailnet.Status{}, err
	}
	return tailnet.FromIPN(i.name, st), nil
}

// LoginError is why the last login attempt failed, or "".
func (i *Instance) LoginError() string {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.upErr
}

// Logout logs the engine out of its tailnet; it keeps running.
func (i *Instance) Logout() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.upCancel != nil {
		i.upCancel()
	}
	if i.srv == nil {
		return nil
	}
	lc, err := i.srv.LocalClient()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return lc.Logout(ctx)
}

// Stop stops the engine and detaches it from the router. The state directory stays.
func (i *Instance) Stop() {
	i.mu.Lock()
	i.stopLocked()
	i.mu.Unlock()
	i.b.mu.Lock()
	if i.b.insts[i.name] == i {
		delete(i.b.insts, i.name)
	}
	i.b.mu.Unlock()
	i.log.Close()
}

func (i *Instance) stopLocked() {
	if i.upCancel != nil {
		i.upCancel()
		i.upCancel = nil
	}
	if i.srv != nil {
		if err := i.srv.Close(); err != nil && !errors.Is(err, context.Canceled) {
			slog.Warn("stop engine", "tailnet", i.name, "err", err)
		}
		i.srv = nil
		i.b.r.RemoveTailnet(i.name)
	}
}

// selfIPs are the engine's own tailnet IPv4 and IPv6, or zero Addrs.
func (i *Instance) selfIPs() (v4, v6 netip.Addr) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.srv == nil {
		return v4, v6
	}
	return i.srv.TailscaleIPs()
}
