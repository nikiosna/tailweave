package tailnet

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"path/filepath"
	"testing"
)

// Trimmed output of /localapi/v0/status from the lab.
const statusJSON = `{
  "BackendState": "Running",
  "MagicDNSSuffix": "b.lab",
  "CurrentTailnet": {"Name": "172.30.0.11", "MagicDNSSuffix": "b.lab", "MagicDNSEnabled": true},
  "Self": {"ID": "6", "HostName": "dut-b", "DNSName": "dut-b.b.lab.",
    "TailscaleIPs": ["100.64.0.6", "fd7a:115c:a1e0::6"], "Online": true,
    "PeerAPIURL": ["http://100.64.0.6:63847", "http://[fd7a:115c:a1e0::6]:56208"]},
  "Peer": {
    "nodekey:aa": {"ID": "2", "HostName": "peer-b2", "DNSName": "peer-b2.b.lab.",
      "TailscaleIPs": ["100.64.0.2", "fd7a:115c:a1e0::2"], "Online": true, "PrimaryRoutes": null}
  }
}`

func TestStatus(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "ts.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/localapi/v0/status" || r.Host != "local-tailscaled.sock" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(statusJSON))
	})}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	st, err := New(sock).Status(context.Background(), "b")
	if err != nil {
		t.Fatal(err)
	}
	tn := st.Tailnet
	if tn.Name != "b" || tn.Domain != "b.lab" || tn.Self.Name != "dut-b" || tn.PeerAPIPort != 63847 || len(tn.Peers) != 1 {
		t.Fatalf("tailnet = %+v", tn)
	}
	p := tn.Peers[0]
	if p.ID != "2" || p.Name != "peer-b2" || p.DNSName != "peer-b2.b.lab." || p.IPv4() != netip.MustParseAddr("100.64.0.2") || !p.Online {
		t.Errorf("peer = %+v", p)
	}
}

func TestNeedsLogin(t *testing.T) {
	s := convert("a", status{BackendState: "NeedsLogin", AuthURL: "http://hs/register/x",
		Self: &node{HostName: "dut-a"}})
	if s.Running() || s.AuthURL != "http://hs/register/x" || s.Tailnet.Name != "" {
		t.Errorf("status = %+v", s)
	}
}

func TestMachineName(t *testing.T) {
	for _, c := range []struct{ host, dns, want string }{
		{"mbp.local", "mbp-tailweave.tailcfeec.ts.net.", "mbp-tailweave"},
		{"dut-a", "", "dut-a"},
	} {
		if got := machineName(&node{HostName: c.host, DNSName: c.dns}); got != c.want {
			t.Errorf("machineName(%q, %q) = %q, want %q", c.host, c.dns, got, c.want)
		}
	}
}
