package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"slices"

	"github.com/Nikiosna/tailweave/internal/instance"
	"github.com/Nikiosna/tailweave/internal/tailnet"
	"github.com/Nikiosna/tailweave/internal/weave"
)

// LoginRequest is the body of POST /api/tailnets: it adds a tailnet or logs an existing
// one in again. Without AuthKey the login is interactive: the tailnet's state shows an
// AuthURL to open.
type LoginRequest struct {
	Name        string `json:"name"`
	LoginServer string `json:"loginServer,omitempty"` // default: DefaultLoginServer, or the stored one
	Hostname    string `json:"hostname,omitempty"`    // default: this machine's host name, or the stored one
	AuthKey     string `json:"authKey,omitempty"`
}

// login adds the tailnet if needed, starts its instance and starts logging in.
func (d *Daemon) login(req LoginRequest) error {
	if err := instance.ValidName(req.Name); err != nil {
		return err
	}
	d.opMu.Lock()
	defer d.opMu.Unlock()
	if !d.up {
		return fmt.Errorf("%w: tailweave is stopped; run `tailweave start` first", errConflict)
	}

	if inst := d.insts[req.Name]; inst != nil {
		if st, err := inst.Status(context.Background()); err == nil && st.Running() {
			return fmt.Errorf("%w: %s is already logged in; use `tailweave remove %s` first to log in differently",
				errConflict, req.Name, req.Name)
		}
	}

	d.mu.Lock()
	i := slices.IndexFunc(d.st.Tailnets, func(e tailnetEntry) bool { return e.Name == req.Name })
	added := i < 0
	if added {
		d.st.Tailnets = append(d.st.Tailnets, tailnetEntry{Name: req.Name, Index: d.freeIndex()})
		i = len(d.st.Tailnets) - 1
	}
	e := &d.st.Tailnets[i]
	e.LoginServer = or(req.LoginServer, or(e.LoginServer, DefaultLoginServer))
	host, _ := os.Hostname()
	e.Hostname = or(req.Hostname, or(e.Hostname, host))
	e.LoggedOut = false
	entry := *e
	d.mu.Unlock()

	inst := d.insts[req.Name]
	if inst == nil {
		if err := d.startInstance(entry); err != nil {
			if added {
				d.forget(req.Name)
			}
			return err
		}
		inst = d.insts[req.Name]
	}
	slog.Info("logging in", "tailnet", req.Name, "server", entry.LoginServer, "authkey", req.AuthKey != "")
	return inst.Up(tailnet.Login{LoginServer: entry.LoginServer, Hostname: entry.Hostname, AuthKey: req.AuthKey})
}

// freeIndex returns the lowest instance index no tailnet uses. Callers hold d.mu.
func (d *Daemon) freeIndex() int {
	for n := 0; ; n++ {
		if !slices.ContainsFunc(d.st.Tailnets, func(e tailnetEntry) bool { return e.Index == n }) {
			return n
		}
	}
}

// logout logs the tailnet out and stops its instance; it stays in the list.
func (d *Daemon) logout(name string) error {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	d.mu.Lock()
	i := slices.IndexFunc(d.st.Tailnets, func(e tailnetEntry) bool { return e.Name == name })
	if i < 0 {
		d.mu.Unlock()
		return fmt.Errorf("%w: no tailnet %q", errNotFound, name)
	}
	d.st.Tailnets[i].LoggedOut = true
	d.mu.Unlock()

	var err error
	if inst := d.insts[name]; inst != nil {
		err = inst.Logout()
	}
	d.stopInstance(name)
	if err != nil {
		slog.Warn("logout", "tailnet", name, "err", err)
	}
	return nil
}

// remove logs the tailnet out and deletes everything tailweave keeps about it.
func (d *Daemon) remove(name string) error {
	d.opMu.Lock()
	defer d.opMu.Unlock()
	d.mu.Lock()
	known := slices.ContainsFunc(d.st.Tailnets, func(e tailnetEntry) bool { return e.Name == name })
	d.mu.Unlock()
	if !known {
		return fmt.Errorf("%w: no tailnet %q", errNotFound, name)
	}
	if inst := d.insts[name]; inst != nil {
		if err := inst.Logout(); err != nil {
			slog.Warn("logout", "tailnet", name, "err", err)
		}
	}
	d.stopInstance(name)
	d.forget(name)
	return d.be.Purge(name)
}

// forget drops a tailnet and its mappings from the state.
func (d *Daemon) forget(name string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.st.Tailnets = slices.DeleteFunc(d.st.Tailnets, func(e tailnetEntry) bool { return e.Name == name })
	d.st.Mappings = slices.DeleteFunc(d.st.Mappings, func(m weave.Mapping) bool { return m.Tailnet == name })
	delete(d.status, name)
	delete(d.tailnets, name)
}
