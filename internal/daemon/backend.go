package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"path/filepath"
	"runtime"

	"github.com/Nikiosna/tailweave/internal/engine"
	"github.com/Nikiosna/tailweave/internal/instance"
	"github.com/Nikiosna/tailweave/internal/netcfg"
	"github.com/Nikiosna/tailweave/internal/tailnet"
	"github.com/Nikiosna/tailweave/internal/weave"
)

// Backends.
const (
	BackendNetns = "netns" // one tailscaled per network namespace (Linux)
	BackendTsnet = "tsnet" // in-process engines behind a userspace router (Linux, macOS)
)

// defaultBackend is the backend used when the config names none.
func defaultBackend() string {
	if runtime.GOOS == "linux" {
		return BackendNetns
	}
	return BackendTsnet
}

// backend runs the tailnets' instances and applies plans to the host. The daemon
// serializes all calls.
type backend interface {
	Cleanup() // removes leftovers of an earlier run, and only tailweave's own
	Setup() error
	Teardown() // also after a failed Setup
	Start(e tailnetEntry) (runner, error)
	Apply(weave.Plan) error
	Purge(name string) error
}

// runner is a tailnet's running instance.
type runner interface {
	Status(ctx context.Context) (tailnet.Status, error)
	Up(tailnet.Login) error
	// SetExitNode makes the instance send internet traffic to the peer with this ID; ""
	// turns that off.
	SetExitNode(ctx context.Context, peerID string) error
	LoginError() string
	Logout() error
	Stop()
}

func newBackend(cfg Config) (backend, error) {
	host, _, err := instance.Addrs(cfg.Transit, 0)
	if err != nil {
		return nil, err
	}
	stateDir := filepath.Join(cfg.StateDir, "tailnets")
	switch cfg.Backend {
	case BackendNetns:
		transit6 := cfg.Transit6
		if !netcfg.IPv6() {
			slog.Warn("IPv6 is off on this host; 4via6 routes will not be reachable")
			transit6 = netip.Prefix{}
		}
		return &netnsBackend{
			mgr: &instance.Manager{
				Transit:  cfg.Transit,
				Transit6: transit6,
				StateDir: stateDir,
				RunDir:   filepath.Join(filepath.Dir(cfg.Socket), "tailnets"),
			},
			host: &netcfg.Host{
				DNSAddr: cfg.DNS,
				Blocked: []netip.Prefix{netcfg.CGNAT, cfg.Pool, weave.ViaRange},
				Transit: cfg.Transit,
				IPv6:    transit6.IsValid(),
			},
		}, nil
	case BackendTsnet:
		routes := []netip.Prefix{netcfg.CGNAT, cfg.Pool}
		host6, _, err := instance.Addrs6(cfg.Transit6, 0)
		if err != nil {
			return nil, err
		}
		if runtime.GOOS == "linux" && !netcfg.IPv6() {
			slog.Warn("IPv6 is off on this host; 4via6 routes will not be reachable")
			host6 = netip.Addr{}
		} else {
			routes = append(routes, weave.ViaRange)
		}
		return &tsnetBackend{&engine.Backend{
			StateDir: stateDir,
			HostIP:   host,
			HostIP6:  host6,
			DNS:      cfg.DNS,
			Routes:   routes,
		}}, nil
	}
	return nil, fmt.Errorf("unknown backend %q", cfg.Backend)
}

// netnsBackend runs one tailscaled per network namespace (package instance) and
// configures the host with package netcfg.
type netnsBackend struct {
	mgr  *instance.Manager
	host *netcfg.Host
}

func (b *netnsBackend) Cleanup()                 { b.host.Teardown() }
func (b *netnsBackend) Setup() error             { return b.host.Setup() }
func (b *netnsBackend) Teardown()                { b.host.Teardown() }
func (b *netnsBackend) Apply(p weave.Plan) error { return b.host.Apply(p) }
func (b *netnsBackend) Purge(name string) error  { return b.mgr.Purge(name) }

func (b *netnsBackend) Start(e tailnetEntry) (runner, error) {
	inst, err := b.mgr.Start(e.Name, e.Index)
	if err != nil {
		return nil, err
	}
	b.host.AddNamespace(inst.Namespace())
	return &netnsRunner{inst, tailnet.New(inst.Socket), b.host}, nil
}

type netnsRunner struct {
	*instance.Instance
	client *tailnet.Client
	host   *netcfg.Host
}

func (r *netnsRunner) Status(ctx context.Context) (tailnet.Status, error) {
	return r.client.Status(ctx, r.Name)
}

func (r *netnsRunner) SetExitNode(ctx context.Context, peerID string) error {
	return r.client.SetExitNode(ctx, peerID)
}

func (r *netnsRunner) Stop() {
	r.host.RemoveNamespace(r.Name)
	r.Instance.Stop()
}

// tsnetBackend runs in-process engines (package engine).
type tsnetBackend struct{ *engine.Backend }

func (b *tsnetBackend) Start(e tailnetEntry) (runner, error) {
	return b.Backend.Start(e.Name, tailnet.Login{LoginServer: e.LoginServer, Hostname: e.Hostname})
}
