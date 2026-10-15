// Package tailnet reads the state of a tailscaled instance through its LocalAPI socket.
//
// Only the few status fields tailweave needs are decoded, which keeps the large
// tailscale.com module out of the dependency tree.
package tailnet

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/Nikiosna/tailweave/internal/weave"
)

// Client talks to one tailscaled.
type Client struct {
	Socket string
	http   *http.Client
}

// New returns a client for the tailscaled listening on the given unix socket.
func New(socket string) *Client {
	return &Client{Socket: socket, http: &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", socket)
		}},
	}}
}

type node struct {
	ID           string
	HostName     string
	DNSName      string
	TailscaleIPs []netip.Addr
	PeerAPIURL   []string // only set for Self, e.g. "http://100.64.0.3:41234"
	Online       bool
}

type status struct {
	BackendState   string
	AuthURL        string
	MagicDNSSuffix string
	CurrentTailnet *struct{ MagicDNSEnabled bool }
	Self           *node
	Peer           map[string]*node
}

// Status is a tailscaled's login state and, while it is Running, its tailnet.
type Status struct {
	Backend string        // tailscaled's BackendState: NeedsLogin, Starting, Running, Stopped, ...
	AuthURL string        // where to log in, while a login is pending
	Tailnet weave.Tailnet // only filled while Running
}

// Running reports whether the instance is logged in and connected.
func (s Status) Running() bool { return s.Backend == "Running" }

// Status returns the instance's state and the tailnet as weave sees it. name is
// tailweave's name for the tailnet.
func (c *Client) Status(ctx context.Context, name string) (Status, error) {
	// The host name is required by tailscaled; it ignores the rest of the URL authority.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://local-tailscaled.sock/localapi/v0/status", nil)
	if err != nil {
		return Status{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return Status{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Status{}, fmt.Errorf("localapi status: %s", resp.Status)
	}
	var st status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return Status{}, fmt.Errorf("decode status: %w", err)
	}
	return convert(name, st), nil
}

func convert(name string, st status) Status {
	s := Status{Backend: st.BackendState, AuthURL: st.AuthURL}
	if !s.Running() {
		return s
	}
	tn := weave.Tailnet{Name: name}
	if st.CurrentTailnet != nil && st.CurrentTailnet.MagicDNSEnabled {
		tn.Domain = st.MagicDNSSuffix
	}
	if st.Self != nil {
		tn.Self = peer(st.Self)
		tn.PeerAPIPort = peerAPIPort(st.Self, tn.Self.IPv4())
	}
	for _, n := range st.Peer {
		tn.Peers = append(tn.Peers, peer(n))
	}
	s.Tailnet = tn
	return s
}

// peerAPIPort returns the port of n's PeerAPI on ip, or 0.
func peerAPIPort(n *node, ip netip.Addr) uint16 {
	for _, s := range n.PeerAPIURL {
		u, err := url.Parse(s)
		if err != nil {
			continue
		}
		ap, err := netip.ParseAddrPort(u.Host)
		if err == nil && ap.Addr() == ip {
			return ap.Port()
		}
	}
	return 0
}

func peer(n *node) weave.Peer {
	return weave.Peer{ID: n.ID, Name: machineName(n), DNSName: n.DNSName, IPs: n.TailscaleIPs, Online: n.Online}
}

// machineName is the name the tailnet knows n by, like `tailscale status` shows it: the
// first label of its MagicDNS name, which follows a rename in the admin console. The
// host name is only what the device reports about itself.
func machineName(n *node) string {
	if name, _, _ := strings.Cut(n.DNSName, "."); name != "" {
		return name
	}
	return n.HostName
}
