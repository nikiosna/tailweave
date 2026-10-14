package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"

	"github.com/Nikiosna/tailweave/internal/weave"
)

// Config is tailweave's configuration file.
type Config struct {
	// Tailnets in priority order: on collisions the first one keeps its IPs and names.
	Tailnets []TailnetConfig `json:"tailnets"`
	AutoMap  *bool           `json:"autoMap"` // initial value; later changes live in the state file (default true)
	Pool     netip.Prefix    `json:"pool"`    // addresses for mappings (default 198.18.0.0/15)
	DNS      netip.Addr      `json:"dns"`     // address of the DNS service (default 100.100.100.100)
	Listen   string          `json:"listen"`  // UI and API (default 127.0.0.1:5250)
	State    string          `json:"state"`   // state file (default state.json next to the config)
}

// TailnetConfig says where a tailnet's tailscaled runs. The namespace is set up by
// someone else for now (lab/scripts/netns-playground.sh).
type TailnetConfig struct {
	Name    string     `json:"name"`
	Netns   string     `json:"netns"`
	Socket  string     `json:"socket"`  // tailscaled LocalAPI socket
	Gateway netip.Addr `json:"gateway"` // the namespace's veth address, seen from the host
	Tun     string     `json:"tun"`     // TUN interface inside the namespace (default ts-<name>)
}

// LoadConfig reads and validates a config file and fills in defaults.
func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, fmt.Errorf("%s: %w", path, err)
	}
	if c.AutoMap == nil {
		t := true
		c.AutoMap = &t
	}
	if !c.Pool.IsValid() {
		c.Pool = netip.MustParsePrefix("198.18.0.0/15")
	}
	if !c.DNS.IsValid() {
		c.DNS = netip.MustParseAddr("100.100.100.100")
	}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:5250"
	}
	if c.State == "" {
		c.State = filepath.Join(filepath.Dir(path), "state.json")
	}
	if len(c.Tailnets) == 0 {
		return c, errors.New("no tailnets configured")
	}
	seen := map[string]bool{}
	for i := range c.Tailnets {
		t := &c.Tailnets[i]
		if t.Name == "" || t.Netns == "" || t.Socket == "" || !t.Gateway.IsValid() {
			return c, fmt.Errorf("tailnet %d: name, netns, socket and gateway are required", i)
		}
		if seen[t.Name] {
			return c, fmt.Errorf("tailnet %q configured twice", t.Name)
		}
		seen[t.Name] = true
		if t.Tun == "" {
			t.Tun = "ts-" + t.Name
		}
	}
	if !c.Pool.Addr().Is4() {
		return c, fmt.Errorf("pool %s is not IPv4", c.Pool)
	}
	return c, nil
}

// state is what tailweave changes at runtime and keeps across restarts.
type state struct {
	AutoMap  bool            `json:"autoMap"`
	Mappings []weave.Mapping `json:"mappings"`
}

func loadState(path string, autoMap bool) (state, error) {
	s := state{AutoMap: autoMap}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

func saveState(path string, s state) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
