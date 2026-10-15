package daemon

import (
	"context"
	"fmt"
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
		return &netnsBackend{
			mgr: &instance.Manager{
				Transit:  cfg.Transit,
				StateDir: stateDir,
				RunDir:   filepath.Join(filepath.Dir(cfg.Socket), "tailnets"),
			},
			host: &netcfg.Host{
				DNSAddr: cfg.DNS,
				Blocked: []netip.Prefix{netcfg.CGNAT, cfg.Pool},
				Transit: cfg.Transit,
			},
		}, nil
	case BackendTsnet:
		return &tsnetBackend{&engine.Backend{
			StateDir: stateDir,
			HostIP:   host,
			DNS:      cfg.DNS,
			Routes:   []netip.Prefix{netcfg.CGNAT, cfg.Pool},
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

func (r *netnsRunner) Stop() {
	r.host.RemoveNamespace(r.Name)
	r.Instance.Stop()
}

// tsnetBackend runs in-process engines (package engine).
type tsnetBackend struct{ *engine.Backend }

func (b *tsnetBackend) Start(e tailnetEntry) (runner, error) {
	return b.Backend.Start(e.Name, tailnet.Login{LoginServer: e.LoginServer, Hostname: e.Hostname})
}
