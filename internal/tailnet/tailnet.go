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
	Online       bool
}

type status struct {
	BackendState   string
	MagicDNSSuffix string
	CurrentTailnet *struct{ MagicDNSEnabled bool }
	Self           *node
	Peer           map[string]*node
}

// Status returns the tailnet as weave sees it. name is tailweave's name for the tailnet.
func (c *Client) Status(ctx context.Context, name string) (weave.Tailnet, error) {
	// The host name is required by tailscaled; it ignores the rest of the URL authority.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://local-tailscaled.sock/localapi/v0/status", nil)
	if err != nil {
		return weave.Tailnet{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return weave.Tailnet{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return weave.Tailnet{}, fmt.Errorf("localapi status: %s", resp.Status)
	}
	var st status
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return weave.Tailnet{}, fmt.Errorf("decode status: %w", err)
	}
	return convert(name, st)
}

func convert(name string, st status) (weave.Tailnet, error) {
	if st.BackendState != "Running" {
		return weave.Tailnet{}, fmt.Errorf("tailscaled is %s, not Running", st.BackendState)
	}
	tn := weave.Tailnet{Name: name}
	if st.CurrentTailnet != nil && st.CurrentTailnet.MagicDNSEnabled {
		tn.Domain = st.MagicDNSSuffix
	}
	if st.Self != nil {
		tn.Self = peer(st.Self)
	}
	for _, n := range st.Peer {
		tn.Peers = append(tn.Peers, peer(n))
	}
	return tn, nil
}

func peer(n *node) weave.Peer {
	return weave.Peer{ID: n.ID, Name: n.HostName, DNSName: n.DNSName, IPs: n.TailscaleIPs, Online: n.Online}
}
