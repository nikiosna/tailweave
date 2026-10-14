package ui

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandler(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "tw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	daemon := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Method+" "+r.URL.Path)
	})}
	go func() { _ = daemon.Serve(ln) }()
	t.Cleanup(func() { _ = daemon.Close() })

	srv := httptest.NewServer(Handler(sock))
	defer srv.Close()

	get := func(path string) (int, string) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get("/"); code != http.StatusOK || !strings.Contains(body, "<html") {
		t.Errorf("GET / = %d %.40q", code, body)
	}
	if code, body := get("/api/state"); code != http.StatusOK || body != "GET /api/state" {
		t.Errorf("GET /api/state = %d %q", code, body)
	}
	if code, _ := get("/other"); code != http.StatusNotFound {
		t.Errorf("GET /other = %d", code)
	}

	_ = daemon.Close()
	if code, body := get("/api/state"); code != http.StatusBadGateway || !strings.Contains(body, sock) {
		t.Errorf("GET /api/state without daemon = %d %q", code, body)
	}
}
