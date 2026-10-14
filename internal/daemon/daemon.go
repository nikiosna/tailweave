// Package daemon runs tailweave: it watches every tailnet instance, computes the merged
// address plan, applies it to the host, serves DNS and offers a JSON API on a Unix socket.
package daemon

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/Nikiosna/tailweave/internal/dns"
	"github.com/Nikiosna/tailweave/internal/netcfg"
	"github.com/Nikiosna/tailweave/internal/tailnet"
	"github.com/Nikiosna/tailweave/internal/weave"
)

// PollInterval is how often the tailnet instances are asked for their peers.
const PollInterval = 2 * time.Second

// Daemon is a running tailweave.
type Daemon struct {
	cfg     Config
	host    *netcfg.Host
	dns     dns.Server
	clients map[string]*tailnet.Client

	reconcileMu sync.Mutex // one reconcile at a time

	mu       sync.Mutex // guards the fields below
	st       state
	tailnets map[string]weave.Tailnet // last good status per tailnet
	errs     map[string]string        // last status error per tailnet
	plan     weave.Plan
}

// New prepares a daemon; nothing is changed on the host until Run.
func New(cfg Config) (*Daemon, error) {
	st, err := loadState(cfg.State, *cfg.AutoMap)
	if err != nil {
		return nil, err
	}
	d := &Daemon{
		cfg:      cfg,
		clients:  map[string]*tailnet.Client{},
		st:       st,
		tailnets: map[string]weave.Tailnet{},
		errs:     map[string]string{},
		host: &netcfg.Host{
			DNSAddr: cfg.DNS,
			Blocked: []netip.Prefix{netcfg.CGNAT, cfg.Pool},
		},
	}
	for _, t := range cfg.Tailnets {
		d.clients[t.Name] = tailnet.New(t.Socket)
		d.host.Namespaces = append(d.host.Namespaces, netcfg.Namespace{
			Tailnet: t.Name, Netns: t.Netns, Gateway: t.Gateway, Tun: t.Tun})
	}
	return d, nil
}

// Run configures the host and keeps it in line with the tailnets until ctx is done.
// Everything is removed again on return.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.host.Setup(); err != nil {
		d.host.Teardown()
		return err
	}
	defer d.host.Teardown()

	if err := d.dns.Start(net.JoinHostPort(d.cfg.DNS.String(), "53")); err != nil {
		return err
	}
	defer d.dns.Close()

	ln, err := listenUnix(d.cfg.Socket)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: d.Handler(), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("api", "err", err)
		}
	}()
	defer srv.Close() // also removes the socket file
	slog.Info("tailweave running", "socket", d.cfg.Socket, "dns", d.cfg.DNS)

	t := time.NewTicker(PollInterval)
	defer t.Stop()
	for {
		if err := d.reconcile(ctx); err != nil {
			slog.Error("reconcile", "err", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
	}
}

// listenUnix listens on a Unix socket only root can use, replacing a stale one.
func listenUnix(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// reconcile fetches every tailnet's status, recomputes the plan and applies it.
func (d *Daemon) reconcile(ctx context.Context) error {
	d.reconcileMu.Lock()
	defer d.reconcileMu.Unlock()

	type result struct {
		tn  weave.Tailnet
		err error
	}
	results := map[string]result{}
	for _, t := range d.cfg.Tailnets {
		tn, err := d.clients[t.Name].Status(ctx, t.Name)
		results[t.Name] = result{tn, err}
	}

	d.mu.Lock()
	for name, r := range results {
		if r.err != nil {
			// Keep the last known peers so a restarting instance does not reshuffle mappings.
			if d.errs[name] != r.err.Error() {
				slog.Warn("tailnet status", "tailnet", name, "err", r.err)
			}
			d.errs[name] = r.err.Error()
			continue
		}
		delete(d.errs, name)
		d.tailnets[name] = r.tn
	}
	d.plan = d.compute()
	if !slices.Equal(d.plan.Mappings, d.st.Mappings) {
		d.st.Mappings = d.plan.Mappings
		if err := saveState(d.cfg.State, d.st); err != nil {
			slog.Error("save state", "err", err)
		}
	}
	plan := d.plan
	d.mu.Unlock()

	d.dns.Update(plan.Names, plan.Domains)
	return d.host.Apply(plan)
}

// compute recomputes the plan from the current state. Callers hold d.mu.
func (d *Daemon) compute() weave.Plan {
	var tns []weave.Tailnet
	for _, t := range d.cfg.Tailnets {
		if tn, ok := d.tailnets[t.Name]; ok {
			tns = append(tns, tn)
		}
	}
	return weave.Compute(tns, d.st.Mappings, weave.Options{AutoMap: d.st.AutoMap, Pool: d.cfg.Pool})
}
