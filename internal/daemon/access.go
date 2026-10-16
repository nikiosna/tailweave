package daemon

import (
	"context"
	"errors"
	"net"
	"net/http"
)

// Operator is the one user besides root who may change tailweave (`tailweave set
// -operator`). Everyone on the machine may read the state.
type Operator struct {
	Name string `json:"name"`
	UID  int    `json:"uid"`
}

type uidKey struct{}

// connContext remembers the user on the other end of an API connection.
func connContext(ctx context.Context, c net.Conn) context.Context {
	uid, err := peerUID(c)
	if err != nil {
		uid = -1
	}
	return context.WithValue(ctx, uidKey{}, uid)
}

// callerUID returns the user who sent r, or -1 if it is not known.
func callerUID(r *http.Request) int {
	if uid, ok := r.Context().Value(uidKey{}).(int); ok {
		return uid
	}
	return -1
}

func isRoot(r *http.Request) bool { return callerUID(r) == 0 }

// mayChange reports whether the caller of r may change tailweave: root or the operator.
func (d *Daemon) mayChange(r *http.Request) bool {
	uid := callerUID(r)
	if uid == 0 {
		return true
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return uid > 0 && d.st.Operator != nil && d.st.Operator.UID == uid
}

// guard lets everyone read and only root and the operator change anything.
func (d *Daemon) guard(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead && !d.mayChange(r) {
			http.Error(w, errNotAllowed.Error(), http.StatusForbidden)
			return
		}
		h.ServeHTTP(w, r)
	})
}

var errNotAllowed = errors.New("only root and the operator may change tailweave; run it with sudo or make yourself the operator: sudo tailweave set -operator=$USER")
