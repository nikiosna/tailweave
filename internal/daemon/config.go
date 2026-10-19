package daemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Nikiosna/tailweave/internal/weave"
)

// DefaultLoginServer is the control server used when a login names none.
const DefaultLoginServer = "https://controlplane.tailscale.com"

// Config is tailweave's configuration file. Every field is optional; tailnets are not
// configured here but added with `tailweave login` and kept in the state.
type Config struct {
	AutoMap  *bool        `json:"autoMap"`  // initial value; later changes live in the state file (default true)
	Pool     netip.Prefix `json:"pool"`     // addresses for mappings (default 198.18.0.0/15)
	DNS      netip.Addr   `json:"dns"`      // address of the DNS service (default 100.100.100.100)
	Socket   string       `json:"socket"`   // Unix socket of the API (default DefaultSocket)
	StateDir string       `json:"stateDir"` // state file and tailscaled state (default /var/lib/tailweave)
	Transit  netip.Prefix `json:"transit"`  // netns: veth addresses, a /30 each; tsnet: the TUN's address is the first host (default 10.201.0.0/16)
	Transit6 netip.Prefix `json:"transit6"` // the same for IPv6, which carries 4via6 routes: a /126 each (default fd74:7765:6176::/64)
	Backend  string       `json:"backend"`  // "netns" or "tsnet" (default: netns on Linux, tsnet elsewhere)

	// Tailnets is no longer supported; it only exists to reject old config files clearly.
	Tailnets json.RawMessage `json:"tailnets,omitempty"`
}

// LoadConfig reads and validates a config file and fills in defaults. A missing file
// at path means all defaults.
func LoadConfig(path string) (Config, error) {
	var c Config
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return c, err
	default:
		if err := json.Unmarshal(b, &c); err != nil {
			return c, fmt.Errorf("%s: %w", path, err)
		}
	}
	if c.Tailnets != nil {
		return c, fmt.Errorf("%s: tailnets are no longer configured in the config file, add them with `tailweave login`", path)
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
	if c.Socket == "" {
		c.Socket = DefaultSocket
	}
	if c.StateDir == "" {
		c.StateDir = defaultStateDir
	}
	if c.Backend == "" {
		c.Backend = defaultBackend()
	}
	if c.Backend != BackendNetns && c.Backend != BackendTsnet {
		return c, fmt.Errorf("unknown backend %q, use %q or %q", c.Backend, BackendNetns, BackendTsnet)
	}
	if c.Backend == BackendNetns && runtime.GOOS != "linux" {
		return c, fmt.Errorf("backend %q needs Linux", BackendNetns)
	}
	if !c.Transit.IsValid() {
		c.Transit = netip.MustParsePrefix("10.201.0.0/16")
	}
	if !c.Transit6.IsValid() {
		c.Transit6 = netip.MustParsePrefix("fd74:7765:6176::/64")
	}
	if !c.Pool.Addr().Is4() {
		return c, fmt.Errorf("pool %s is not IPv4", c.Pool)
	}
	if !c.Transit.Addr().Is4() || c.Transit.Bits() > 30 {
		return c, fmt.Errorf("transit %s must be IPv4 and hold at least one /30", c.Transit)
	}
	if !c.Transit6.Addr().Is6() || c.Transit6.Addr().Is4In6() || c.Transit6.Bits() > 126 {
		return c, fmt.Errorf("transit6 %s must be IPv6 and hold at least one /126", c.Transit6)
	}
	return c, nil
}

func (c Config) statePath() string { return filepath.Join(c.StateDir, "state.json") }

// state is what tailweave changes at runtime and keeps across restarts.
type state struct {
	AutoMap  bool            `json:"autoMap"`
	Tailnets []tailnetEntry  `json:"tailnets"` // in priority order: the first keeps colliding IPs
	Mappings []weave.Mapping `json:"mappings"`
	Operator *Operator       `json:"operator,omitempty"`
}

// tailnetEntry is a tailnet added with `tailweave login`.
type tailnetEntry struct {
	Name        string `json:"name"`
	LoginServer string `json:"loginServer"`
	Hostname    string `json:"hostname"`
	Index       int    `json:"index"`               // picks the veth names and transit /30
	LoggedOut   bool   `json:"loggedOut,omitempty"` // no instance runs until the next login
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
