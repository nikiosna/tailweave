package daemon

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAccess calls the API over a real Unix socket as the user running the test, who is
// neither root nor, at first, the operator.
func TestAccess(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("needs a user other than root")
	}
	dir := t.TempDir()
	d := &Daemon{cfg: Config{StateDir: dir}}
	d.status = map[string]tailnetStatus{"a": {backend: "NeedsLogin", authURL: "https://login/x"}}
	d.st.Tailnets = []tailnetEntry{{Name: "a"}}

	sock := filepath.Join(dir, "tw.sock")
	ln, err := listenUnix(sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: d.Handler(), ConnContext: connContext}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	hc := &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", sock)
	}}}
	call := func(method, path, body string) (int, string) {
		req, _ := http.NewRequest(method, "http://tw"+path, strings.NewReader(body))
		resp, err := hc.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}

	if code, body := call("GET", "/api/state", ""); code != http.StatusOK || strings.Contains(body, "login/x") {
		t.Errorf("state as a stranger: %d %s, want 200 without the login URL", code, body)
	}
	if code, _ := call("PUT", "/api/options", `{"autoMap":true}`); code != http.StatusForbidden {
		t.Errorf("change as a stranger: %d, want 403", code)
	}
	if code, _ := call("PUT", "/api/options", `{"operator":{"name":"me","uid":1}}`); code != http.StatusForbidden {
		t.Errorf("setting the operator as a stranger: %d, want 403", code)
	}

	d.st.Operator = &Operator{Name: "me", UID: os.Getuid()}
	if code, body := call("GET", "/api/state", ""); code != http.StatusOK || !strings.Contains(body, "login/x") {
		t.Errorf("state as the operator: %d %s, want 200 with the login URL", code, body)
	}
	if code, _ := call("PUT", "/api/options", `{"operator":{"name":""}}`); code != http.StatusForbidden {
		t.Errorf("operator removing itself: %d, want 403 (root only)", code)
	}
	// Gets past the guard and fails only because there is no such mapping.
	if code, body := call("DELETE", "/api/mappings/a/x", ""); code != http.StatusNotFound {
		t.Errorf("change as the operator: %d %s, want 404", code, body)
	}
}
