// Package daemon runs tailweave: it runs one Tailscale instance per tailnet, isolated
// from the others (backend netns: a tailscaled per network namespace; backend tsnet:
// in-process engines behind a userspace router), computes the merged address plan,
// applies it to the host, serves DNS and offers a JSON API on a Unix socket.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/Nikiosna/tailweave/internal/dns"
	"github.com/Nikiosna/tailweave/internal/instance"
	"github.com/Nikiosna/tailweave/internal/netcfg"
	"github.com/Nikiosna/tailweave/internal/preflight"
	"github.com/Nikiosna/tailweave/internal/tailnet"
	"github.com/Nikiosna/tailweave/internal/weave"
)

// PollInterval is how often the tailnet instances are asked for their peers.
const PollInterval = 2 * time.Second

// Daemon is a running tailweave.
type Daemon struct {
	cfg Config
	dns dns.Server

	// opMu serializes reconciling and starting/stopping instances. It guards the fields
	// below and is taken before mu.
	opMu  sync.Mutex
	be    backend
	insts map[string]runner

	mu       sync.Mutex // guards the fields below
	st       state
	tailnets map[string]weave.Tailnet // last good status per running tailnet
	status   map[string]tailnetStatus
	plan     weave.Plan
}

// tailnetStatus is the login state of a tailnet's instance, as of the last poll.
type tailnetStatus struct {
	backend    string // tailscaled's BackendState, or Down / LoggedOut
	authURL    string
	err        string // instance not reachable or not startable
	loginError string // last login attempt failed
}

// New prepares a daemon; nothing is changed on the host until Run.
func New(cfg Config) (*Daemon, error) {
	st, err := loadState(cfg.statePath(), *cfg.AutoMap)
	if err != nil {
		return nil, err
	}
	be, err := newBackend(cfg)
	if err != nil {
		return nil, err
	}
	return &Daemon{
		cfg:      cfg,
		be:       be,
		insts:    map[string]runner{},
		st:       st,
		tailnets: map[string]weave.Tailnet{},
		status:   map[string]tailnetStatus{},
	}, nil
}

// Run configures the host, starts the instances of all logged-in tailnets and keeps the
// host in line with them until ctx is done. Everything is removed again on return,
// except the tailscaled state, so the tailnets stay logged in.
func (d *Daemon) Run(ctx context.Context) error {
	// Remove what a crashed run left, then make sure nothing else uses what tailweave is
	// about to set up: another VPN client and tailweave would break each other.
	d.be.Cleanup()
	if err := preflight.Run(d.claims()); err != nil {
		return err
	}
	if err := d.be.Setup(); err != nil {
		d.be.Teardown()
		return err
	}
	defer d.be.Teardown()

	if err := d.dns.Start(net.JoinHostPort(d.cfg.DNS.String(), "53")); err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("another program already serves DNS on %s:53 (another VPN client or DNS proxy?); stop it before starting tailweave: %w", d.cfg.DNS, err)
		}
		return err
	}
	defer d.dns.Close()

	ln, err := listenUnix(d.cfg.Socket)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: d.Handler(), ConnContext: connContext, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("api", "err", err)
		}
	}()
	defer srv.Close() // also removes the socket file

	d.opMu.Lock()
	for _, e := range d.entries() {
		if !e.LoggedOut {
			d.startInstance(e)
		}
	}
	d.opMu.Unlock()
	defer d.stopAll()
	slog.Info("tailweave running", "backend", d.cfg.Backend, "socket", d.cfg.Socket, "dns", d.cfg.DNS, "tailnets", len(d.insts))

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

// claims returns the host resources tailweave is about to use.
func (d *Daemon) claims() preflight.Claims {
	c := preflight.Claims{
		Ranges:        []netip.Prefix{netcfg.CGNAT, d.cfg.Pool, d.cfg.Transit},
		DNS:           d.cfg.DNS,
		OwnInterfaces: []string{netcfg.VethPrefix, netcfg.DNSLink},
	}
	if runtime.GOOS == "linux" {
		c.RulePref, c.Table = netcfg.RulePref, strconv.Itoa(netcfg.Table)
	}
	if host, _, err := instance.Addrs(d.cfg.Transit, 0); err == nil {
		c.OwnAddr = host
	}
	return c
}

// listenUnix listens on a Unix socket every user can connect to, replacing a stale one.
// The API checks who is calling (guard).
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
	if err := os.Chmod(path, 0o666); err != nil {
		ln.Close()
		return nil, err
	}
	return ln, nil
}

// entries returns a copy of the stored tailnets.
func (d *Daemon) entries() []tailnetEntry {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.st.Tailnets)
}

// startInstance starts a tailnet's instance; a failure shows up in its status. Callers
// hold opMu.
func (d *Daemon) startInstance(e tailnetEntry) error {
	inst, err := d.be.Start(e)
	if err != nil {
		slog.Error("start instance", "tailnet", e.Name, "err", err)
		d.setStatus(e.Name, tailnetStatus{backend: "Down", err: err.Error()})
		return err
	}
	d.insts[e.Name] = inst
	return nil
}

// stopInstance stops a tailnet's instance and forgets its peers. Callers hold opMu.
func (d *Daemon) stopInstance(name string) {
	if inst := d.insts[name]; inst != nil {
		inst.Stop()
	}
	delete(d.insts, name)
	d.mu.Lock()
	delete(d.tailnets, name)
	delete(d.status, name)
	d.mu.Unlock()
}

func (d *Daemon) stopAll() {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	for name := range d.insts {
		d.stopInstance(name)
	}
}

func (d *Daemon) setStatus(name string, s tailnetStatus) {
	d.mu.Lock()
	d.status[name] = s
	d.mu.Unlock()
}

// reconcile fetches every tailnet's status, recomputes the plan and applies it.
func (d *Daemon) reconcile(ctx context.Context) error {
	d.opMu.Lock()
	defer d.opMu.Unlock()

	type result struct {
		st  tailnet.Status
		err error
	}
	results := map[string]result{}
	for name, inst := range d.insts {
		st, err := inst.Status(ctx)
		results[name] = result{st, err}
	}

	d.mu.Lock()
	for _, e := range d.st.Tailnets {
		r, ok := results[e.Name]
		switch {
		case !ok:
			// No instance: logged out, or it failed to start (status set then).
			delete(d.tailnets, e.Name)
			if e.LoggedOut {
				d.status[e.Name] = tailnetStatus{backend: "LoggedOut"}
			}
		case r.err != nil:
			// Keep the last known peers so a restarting instance does not reshuffle mappings.
			if d.status[e.Name].err != r.err.Error() {
				slog.Warn("tailnet status", "tailnet", e.Name, "err", r.err)
			}
			d.status[e.Name] = tailnetStatus{backend: "Down", err: r.err.Error(),
				loginError: d.insts[e.Name].LoginError()}
		default:
			d.status[e.Name] = tailnetStatus{backend: r.st.Backend, authURL: r.st.AuthURL,
				loginError: d.insts[e.Name].LoginError()}
			if r.st.Running() {
				d.tailnets[e.Name] = r.st.Tailnet
			} else {
				delete(d.tailnets, e.Name)
			}
		}
	}
	d.plan = d.compute()
	if !slices.Equal(d.plan.Mappings, d.st.Mappings) {
		d.st.Mappings = d.plan.Mappings
		if err := saveState(d.cfg.statePath(), d.st); err != nil {
			slog.Error("save state", "err", err)
		}
	}
	plan := d.plan
	d.mu.Unlock()

	d.dns.Update(plan.Names, plan.Domains)
	return d.be.Apply(plan)
}

// compute recomputes the plan from the current state. Callers hold d.mu.
func (d *Daemon) compute() weave.Plan {
	var tns []weave.Tailnet
	for _, e := range d.st.Tailnets {
		if tn, ok := d.tailnets[e.Name]; ok {
			tns = append(tns, tn)
		}
	}
	return weave.Compute(tns, d.st.Mappings, weave.Options{AutoMap: d.st.AutoMap, Pool: d.cfg.Pool})
}
