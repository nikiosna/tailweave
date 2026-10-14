package labtest

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// keys reads the preauth and API keys bootstrap.sh wrote, as KEY_A, APIKEY_A and so on.
func keys() (map[string]string, error) {
	b, err := os.ReadFile(filepath.Join(Root(), "lab", ".state", "keys.env"))
	if err != nil {
		return nil, err
	}
	m := map[string]string{}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok {
			m[k] = v
		}
	}
	return m, nil
}

func key(tailnet, kind string) (string, error) {
	m, err := keys()
	if err != nil {
		return "", fmt.Errorf("lab keys: %w (run `make lab-up`)", err)
	}
	name := kind + "_" + strings.ToUpper(tailnet)
	if m[name] == "" {
		return "", fmt.Errorf("no %s in lab/.state/keys.env (run `make lab-up` again)", name)
	}
	return m[name], nil
}

var httpc = &http.Client{Timeout: 10 * time.Second}

// api calls headscale's REST API of a tailnet and decodes the answer into out, if given.
func api(tailnet, method, path string, out any) error {
	k, err := key(tailnet, "APIKEY")
	if err != nil {
		return err
	}
	req, err := http.NewRequest(method, LoginServer(tailnet)+"/api/v1/"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+k)
	resp, err := httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("headscale-%s %s %s: %s: %s", tailnet, method, path, resp.Status, b)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

func nodes(tailnet string) ([]Node, error) {
	var r struct{ Nodes []Node }
	err := api(tailnet, http.MethodGet, "node", &r)
	return r.Nodes, err
}

// Nodes lists all nodes of a lab tailnet.
func Nodes(t testing.TB, tailnet string) []Node {
	t.Helper()
	n, err := nodes(tailnet)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// NodeIP returns the IPv4 address of the named node in a tailnet.
func NodeIP(t testing.TB, tailnet, name string) netip.Addr {
	t.Helper()
	for _, n := range Nodes(t, tailnet) {
		if n.Name == name {
			return n.IPv4()
		}
	}
	t.Fatalf("node %s not found in tailnet %s", name, tailnet)
	return netip.Addr{}
}

// RequireLab skips the test if the lab is not running or not reachable.
func RequireLab(t testing.TB) {
	t.Helper()
	for _, tn := range []string{TailnetA, TailnetB} {
		if _, err := nodes(tn); err != nil {
			t.Skipf("lab not usable (%v), run `make lab-up`", err)
		}
	}
}

// AuthKeyFile writes the lab's reusable preauth key for a tailnet into a file in dir and
// returns its path.
func AuthKeyFile(t testing.TB, dir, tailnet string) string {
	t.Helper()
	k, err := key(tailnet, "KEY")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "authkey-"+tailnet)
	if err := os.WriteFile(path, []byte(k), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// ForgetNodes deletes the nodes whose name starts with prefix from a tailnet, so earlier
// test runs do not linger as offline peers.
func ForgetNodes(t testing.TB, tailnet, prefix string) {
	t.Helper()
	for _, n := range Nodes(t, tailnet) {
		if strings.HasPrefix(n.Name, prefix) {
			if err := api(tailnet, http.MethodDelete, "node/"+n.ID, nil); err != nil {
				t.Error(err)
			}
		}
	}
}
