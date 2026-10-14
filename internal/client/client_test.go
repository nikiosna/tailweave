package client

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Nikiosna/tailweave/internal/daemon"
)

// fakeDaemon serves h on a Unix socket and returns the socket path.
func fakeDaemon(t *testing.T, h http.HandlerFunc) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "tw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func TestRequests(t *testing.T) {
	var got []string
	sock := fakeDaemon(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		b, _ := json.Marshal(body)
		got = append(got, r.Method+" "+r.URL.EscapedPath()+" "+string(b))
		_ = json.NewEncoder(w).Encode(daemon.State{AutoMap: true})
	})
	c := New(sock)
	ctx := context.Background()
	if s, err := c.State(ctx); err != nil || !s.AutoMap {
		t.Fatalf("State = %+v, %v", s, err)
	}
	if _, err := c.SetAutoMap(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Map(ctx, "b", "n1", netip.MustParseAddr("198.18.0.42")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Unmap(ctx, "b", "n/1"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"GET /api/state null",
		`PUT /api/options {"autoMap":false}`,
		`POST /api/mappings {"ip":"198.18.0.42","peer":"n1","tailnet":"b"}`,
		"DELETE /api/mappings/b/n%2F1 null",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("requests:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestErrors(t *testing.T) {
	sock := fakeDaemon(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "198.18.0.42 is not free", http.StatusBadRequest)
	})
	if _, err := New(sock).Map(context.Background(), "b", "n1", netip.Addr{}); err == nil || err.Error() != "198.18.0.42 is not free" {
		t.Errorf("err = %v", err)
	}
	_, err := New(filepath.Join(t.TempDir(), "missing.sock")).State(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("err = %v", err)
	}
}
