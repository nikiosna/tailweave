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
	Operator   *Operator        `json:"operator,omitempty"`
}

// TailnetState is a tailnet's own status.
type TailnetState struct {
	Name        string     `json:"name"`
	LoginServer string     `json:"loginServer"`
	State       string     `json:"state"`             // Running, NeedsLogin, Starting, Down, LoggedOut, ...
	AuthURL     string     `json:"authURL,omitempty"` // open it to complete a pending login; only for root and the operator
	Domain      string     `json:"domain"`
	Self        string     `json:"self"` // this machine's name in the tailnet
	IP          netip.Addr `json:"ip"`   // this machine's IP in the tailnet
	Error       string     `json:"error,omitempty"`
	LoginError  string     `json:"loginError,omitempty"` // why the last login attempt failed
}

// MappingRequest is the body of POST /api/mappings. It maps a device (PeerID) to IP, or
// a subnet route (Route) to Prefix. Without IP or Prefix, free pool addresses are used.
type MappingRequest struct {
	Tailnet string       `json:"tailnet"`
	PeerID  string       `json:"peer,omitempty"`
	IP      netip.Addr   `json:"ip,omitzero"`
	Route   netip.Prefix `json:"route,omitzero"`
	Prefix  netip.Prefix `json:"prefix,omitzero"`
}

// Options is the body of PUT /api/options; fields left out stay as they are.
type Options struct {
	AutoMap *bool `json:"autoMap,omitempty"`
	// Operator replaces the operator; one without a name removes it. Only root may set it.
	Operator *Operator `json:"operator,omitempty"`
}

// Handler serves the JSON API used by the CLI and by `tailweave ui`. Everyone may read,
// only root and the operator may change anything.
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, d.stateFor(r))
	})
	mux.HandleFunc("PUT /api/options", func(w http.ResponseWriter, r *http.Request) {
		var o Options
		if !readJSON(w, r, &o) {
			return
		}
		if o.Operator != nil && !isRoot(r) {
			http.Error(w, "only root may set the operator", http.StatusForbidden)
			return
		}
		d.change(w, r, func() error {
			if o.AutoMap != nil {
				d.st.AutoMap = *o.AutoMap
			}
			if o.Operator != nil {
				d.st.Operator = nil
				if o.Operator.Name != "" {
					if o.Operator.UID <= 0 {
						return fmt.Errorf("operator %q needs a user ID other than root's", o.Operator.Name)
					}
					op := *o.Operator
					d.st.Operator = &op
				}
			}
			return nil
		})
	})
	mux.HandleFunc("POST /api/mappings", func(w http.ResponseWriter, r *http.Request) {
		var m MappingRequest
		if !readJSON(w, r, &m) {
			return
		}
		d.change(w, r, func() error {
			if m.Route.IsValid() {
				return d.setRouteMapping(m)
			}
			return d.setMapping(m)
		})
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
		tailnet, peer := r.PathValue("tailnet"), r.PathValue("peer")
		d.change(w, r, func() error {
			return d.deleteMapping(func(m weave.Mapping) bool {
				return m.Tailnet == tailnet && m.PeerID == peer && !m.Route.IsValid()
			})
		})
	})
	// The route is the rest of the path, e.g. /api/routes/b/10.99.0.0/24.
	mux.HandleFunc("DELETE /api/routes/{tailnet}/{route...}", func(w http.ResponseWriter, r *http.Request) {
		tailnet := r.PathValue("tailnet")
		route, err := netip.ParsePrefix(r.PathValue("route"))
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		d.change(w, r, func() error {
			return d.deleteMapping(func(m weave.Mapping) bool {
				return m.Tailnet == tailnet && m.Route.Masked() == route.Masked()
			})
		})
	})
	return d.guard(mux)
}

// stateFor returns the state as the caller of r may see it: a pending login URL would let
// anyone who opens it decide whose account the machine joins, so only root and the
// operator get it.
func (d *Daemon) stateFor(r *http.Request) State {
	s := d.State()
	if !d.mayChange(r) {
		for i := range s.Tailnets {
			s.Tailnets[i].AuthURL = ""
		}
	}
	return s
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
		Operator:   d.st.Operator,
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

// setRouteMapping replaces the mapping of a subnet route with a manual one. Callers hold d.mu.
func (d *Daemon) setRouteMapping(req MappingRequest) error {
	req.Route = req.Route.Masked()
	var cur *weave.Route
	for _, dev := range d.plan.Devices {
		for i, r := range dev.Routes {
			if dev.Tailnet == req.Tailnet && r.Prefix == req.Route {
				cur = &dev.Routes[i]
			}
		}
	}
	if cur == nil {
		return errNotFound
	}
	if !req.Route.Addr().Is4() {
		return fmt.Errorf("only IPv4 routes can be mapped; %s is unique already", req.Route)
	}
	if !req.Prefix.IsValid() {
		req.Prefix = d.plan.SuggestPrefix(d.cfg.Pool, req.Route.Bits())
		if !req.Prefix.IsValid() {
			return fmt.Errorf("address pool %s has no free /%d", d.cfg.Pool, req.Route.Bits())
		}
	}
	req.Prefix = req.Prefix.Masked()
	if req.Prefix.Bits() != req.Route.Bits() {
		return fmt.Errorf("%s and %s differ in size", req.Prefix, req.Route)
	}
	// The route's own current mapping does not block its block (auto -> manual).
	own := cur.Mapping != nil && cur.Mapping.Prefix == req.Prefix
	if !own && !d.plan.FreePrefix(req.Prefix) {
		return fmt.Errorf("%s is not free", req.Prefix)
	}
	d.st.Mappings = append(slices.DeleteFunc(d.st.Mappings, func(m weave.Mapping) bool {
		return m.Tailnet == req.Tailnet && m.Route.Masked() == req.Route
	}), weave.Mapping{Tailnet: req.Tailnet, Route: req.Route, Prefix: req.Prefix})
	return nil
}

// deleteMapping removes the mappings matching del. Callers hold d.mu.
func (d *Daemon) deleteMapping(del func(weave.Mapping) bool) error {
	n := len(d.st.Mappings)
	d.st.Mappings = slices.DeleteFunc(d.st.Mappings, del)
	if len(d.st.Mappings) == n {
		return errNotFound
	}
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
	writeJSON(w, d.stateFor(r))
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
