// Package client talks to a running tailweave daemon through its Unix socket.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/Nikiosna/tailweave/internal/daemon"
)

// Client is a connection to the daemon's API.
type Client struct {
	Socket string
	http   *http.Client
}

// New returns a client for the daemon listening on socket.
func New(socket string) *Client {
	// Changes wait for the daemon to apply them, so allow more than a status read.
	return &Client{Socket: socket, http: &http.Client{Timeout: 30 * time.Second, Transport: Transport(socket)}}
}

// Transport sends every request to the Unix socket, whatever the URL's host.
func Transport(socket string) *http.Transport {
	return &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", socket)
	}}
}

// State returns the daemon's current view of the merged tailnets.
func (c *Client) State(ctx context.Context) (daemon.State, error) {
	return c.do(ctx, http.MethodGet, "/api/state", nil)
}

// SetAutoMap turns automatic remapping of colliding devices on or off.
func (c *Client) SetAutoMap(ctx context.Context, on bool) (daemon.State, error) {
	return c.do(ctx, http.MethodPut, "/api/options", daemon.Options{AutoMap: &on})
}

// SetOperator lets the user op change tailweave without root; an empty name removes the
// operator. Only root may call it.
func (c *Client) SetOperator(ctx context.Context, op daemon.Operator) (daemon.State, error) {
	return c.do(ctx, http.MethodPut, "/api/options", daemon.Options{Operator: &op})
}

// Map routes a device at ip; without a valid ip the daemon picks a free pool address.
func (c *Client) Map(ctx context.Context, tailnet, peerID string, ip netip.Addr) (daemon.State, error) {
	return c.do(ctx, http.MethodPost, "/api/mappings", daemon.MappingRequest{Tailnet: tailnet, PeerID: peerID, IP: ip})
}

// MapRoute routes a tailnet's subnet route at prefix; without a valid prefix the daemon
// picks a free pool block.
func (c *Client) MapRoute(ctx context.Context, tailnet string, route, prefix netip.Prefix) (daemon.State, error) {
	return c.do(ctx, http.MethodPost, "/api/mappings", daemon.MappingRequest{Tailnet: tailnet, Route: route, Prefix: prefix})
}

// UnmapRoute removes the mapping of a tailnet's subnet route.
func (c *Client) UnmapRoute(ctx context.Context, tailnet string, route netip.Prefix) (daemon.State, error) {
	return c.do(ctx, http.MethodDelete, "/api/routes/"+url.PathEscape(tailnet)+"/"+route.String(), nil)
}

// Unmap removes a device's mapping.
func (c *Client) Unmap(ctx context.Context, tailnet, peerID string) (daemon.State, error) {
	return c.do(ctx, http.MethodDelete, "/api/mappings/"+url.PathEscape(tailnet)+"/"+url.PathEscape(peerID), nil)
}

// Login adds a tailnet or logs it in again; it returns once the login has started.
func (c *Client) Login(ctx context.Context, req daemon.LoginRequest) (daemon.State, error) {
	return c.do(ctx, http.MethodPost, "/api/tailnets", req)
}

// Logout logs a tailnet out and stops its instance; it stays known to tailweave.
func (c *Client) Logout(ctx context.Context, name string) (daemon.State, error) {
	return c.do(ctx, http.MethodPost, "/api/tailnets/"+url.PathEscape(name)+"/logout", nil)
}

// Stop removes tailweave from the host until Start, so another VPN can run; the
// daemon stays stopped across restarts.
func (c *Client) Stop(ctx context.Context) (daemon.State, error) {
	return c.do(ctx, http.MethodPost, "/api/stop", nil)
}

// Start switches a stopped tailweave on again. It fails if another VPN is in the way.
func (c *Client) Start(ctx context.Context) (daemon.State, error) {
	return c.do(ctx, http.MethodPost, "/api/start", nil)
}

// Remove logs a tailnet out and deletes all that tailweave keeps about it.
func (c *Client) Remove(ctx context.Context, name string) (daemon.State, error) {
	return c.do(ctx, http.MethodDelete, "/api/tailnets/"+url.PathEscape(name), nil)
}

func (c *Client) do(ctx context.Context, method, path string, body any) (daemon.State, error) {
	var s daemon.State
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return s, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://tailweave"+path, rd)
	if err != nil {
		return s, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var op *net.OpError
		if errors.As(err, &op) && op.Op == "dial" {
			return s, fmt.Errorf("tailweave is not reachable at %s (is `tailweave run` running?): %w", c.Socket, op.Err)
		}
		return s, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return s, errors.New(strings.TrimSpace(string(msg)))
	}
	return s, json.NewDecoder(resp.Body).Decode(&s)
}
