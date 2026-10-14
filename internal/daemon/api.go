package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"slices"

	"github.com/Nikiosna/tailweave/internal/weave"
)

// State is the answer of GET /api/state.
type State struct {
	AutoMap    bool             `json:"autoMap"`
	Pool       netip.Prefix     `json:"pool"`
	DNS        netip.Addr       `json:"dns"`
	Suggestion netip.Addr       `json:"suggestion"` // free pool IP for the next mapping
	Tailnets   []TailnetState   `json:"tailnets"`
	Devices    []weave.Device   `json:"devices"`
	Conflicts  []weave.Conflict `json:"conflicts"`
	Mappings   []weave.Mapping  `json:"mappings"`
}

// TailnetState is a tailnet's own status.
type TailnetState struct {
	Name        string     `json:"name"`
	LoginServer string     `json:"loginServer"`
	State       string     `json:"state"`             // Running, NeedsLogin, Starting, Down, LoggedOut, ...
	AuthURL     string     `json:"authURL,omitempty"` // open it to complete a pending login
	Domain      string     `json:"domain"`
	Self        string     `json:"self"` // this machine's name in the tailnet
	IP          netip.Addr `json:"ip"`   // this machine's IP in the tailnet
	Error       string     `json:"error,omitempty"`
	LoginError  string     `json:"loginError,omitempty"` // why the last login attempt failed
}

// MappingRequest is the body of POST /api/mappings. Without IP, a free pool address is used.
type MappingRequest struct {
	Tailnet string     `json:"tailnet"`
	PeerID  string     `json:"peer"`
	IP      netip.Addr `json:"ip"`
}

// Handler serves the JSON API used by the CLI and by `tailweave ui`.
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, d.State())
	})
	mux.HandleFunc("PUT /api/options", func(w http.ResponseWriter, r *http.Request) {
		var o struct {
			AutoMap *bool `json:"autoMap"`
		}
		if !readJSON(w, r, &o) {
			return
		}
		d.change(w, r, func() error {
			if o.AutoMap != nil {
				d.st.AutoMap = *o.AutoMap
			}
			return nil
		})
	})
	mux.HandleFunc("POST /api/mappings", func(w http.ResponseWriter, r *http.Request) {
		var m MappingRequest
		if !readJSON(w, r, &m) {
			return
		}
		d.change(w, r, func() error { return d.setMapping(m) })
	})
	mux.HandleFunc("POST /api/tailnets", func(w http.ResponseWriter, r *http.Request) {
		var req LoginRequest
		if !readJSON(w, r, &req) {
			return
		}
		d.tailnetChange(w, r, func() error { return d.login(req) })
	})
	mux.HandleFunc("POST /api/tailnets/{name}/logout", func(w http.ResponseWriter, r *http.Request) {
		d.tailnetChange(w, r, func() error { return d.logout(r.PathValue("name")) })
	})
	mux.HandleFunc("DELETE /api/tailnets/{name}", func(w http.ResponseWriter, r *http.Request) {
		d.tailnetChange(w, r, func() error { return d.remove(r.PathValue("name")) })
	})
	mux.HandleFunc("DELETE /api/mappings/{tailnet}/{peer}", func(w http.ResponseWriter, r *http.Request) {
		key := weave.Mapping{Tailnet: r.PathValue("tailnet"), PeerID: r.PathValue("peer")}
		d.change(w, r, func() error {
			n := len(d.st.Mappings)
			d.st.Mappings = slices.DeleteFunc(d.st.Mappings, func(m weave.Mapping) bool {
				return m.Tailnet == key.Tailnet && m.PeerID == key.PeerID
			})
			if len(d.st.Mappings) == n {
				return errNotFound
			}
			return nil
		})
	})
	return mux
}

// State returns a snapshot of the merged tailnets.
func (d *Daemon) State() State {
	d.mu.Lock()
	defer d.mu.Unlock()
	s := State{
		AutoMap:    d.st.AutoMap,
		Pool:       d.cfg.Pool,
		DNS:        d.cfg.DNS,
		Suggestion: d.plan.Suggest(d.cfg.Pool),
		Devices:    d.plan.Devices,
		Conflicts:  d.plan.Conflicts,
		Mappings:   d.st.Mappings,
	}
	for _, e := range d.st.Tailnets {
		tn, st := d.tailnets[e.Name], d.status[e.Name]
		s.Tailnets = append(s.Tailnets, TailnetState{
			Name: e.Name, LoginServer: e.LoginServer, State: or(st.backend, "Starting"), AuthURL: st.authURL,
			Domain: tn.Domain, Self: tn.Self.Name, IP: tn.Self.IPv4(), Error: st.err, LoginError: st.loginError})
	}
	return s
}

var (
	errNotFound = errors.New("not found")
	errConflict = errors.New("conflict")
)

// setMapping replaces the mapping of a device with a manual one. Callers hold d.mu.
func (d *Daemon) setMapping(req MappingRequest) error {
	var dev *weave.Device
	for i := range d.plan.Devices {
		if x := &d.plan.Devices[i]; x.Tailnet == req.Tailnet && x.PeerID == req.PeerID {
			dev = x
		}
	}
	if dev == nil {
		return errNotFound
	}
	if !req.IP.IsValid() {
		req.IP = d.plan.Suggest(d.cfg.Pool)
		if !req.IP.IsValid() {
			return fmt.Errorf("address pool %s is exhausted", d.cfg.Pool)
		}
	}
	// The device's own current mapping does not block its IP (auto -> manual).
	own := dev.Mapping != nil && dev.Mapping.IP == req.IP
	if !own && !d.plan.Free(req.IP) {
		return fmt.Errorf("%s is not free", req.IP)
	}
	d.st.Mappings = append(slices.DeleteFunc(d.st.Mappings, func(m weave.Mapping) bool {
		return m.Tailnet == req.Tailnet && m.PeerID == req.PeerID
	}), weave.Mapping{Tailnet: req.Tailnet, PeerID: req.PeerID, IP: req.IP})
	return nil
}

// change applies a modification of the state, recomputes and answers with the new state.
func (d *Daemon) change(w http.ResponseWriter, r *http.Request, f func() error) {
	d.mu.Lock()
	err := f()
	if err == nil {
		err = d.commit()
	}
	d.mu.Unlock()
	d.answer(w, r, err)
}

// tailnetChange runs f, which starts or stops instances and takes the locks it needs,
// then recomputes and answers with the new state.
func (d *Daemon) tailnetChange(w http.ResponseWriter, r *http.Request, f func() error) {
	err := f()
	if err == nil {
		d.mu.Lock()
		err = d.commit()
		d.mu.Unlock()
	}
	d.answer(w, r, err)
}

// commit recomputes the plan and saves the state. Callers hold d.mu.
func (d *Daemon) commit() error {
	d.plan = d.compute()
	d.st.Mappings = d.plan.Mappings
	return saveState(d.cfg.statePath(), d.st)
}

// answer reports err, or applies the change and answers with the new state.
func (d *Daemon) answer(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errNotFound):
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	case errors.Is(err, errConflict):
		http.Error(w, err.Error(), http.StatusConflict)
		return
	case err != nil:
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := d.reconcile(r.Context()); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, d.State())
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(v); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
