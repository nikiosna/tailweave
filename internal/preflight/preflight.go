// Package preflight checks before tailweave touches the host whether another program,
// typically another VPN client such as Tailscale, already uses the addresses, routes or
// policy routing rules tailweave needs. Two such programs would break each other's
// routing in ways that are hard to diagnose, so tailweave refuses to start instead.
//
// Check is pure and works on a Snapshot; Collect takes the snapshot on the running
// system. Leftovers of an earlier tailweave run must have been removed before.
package preflight

import (
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
)

// Claims is what tailweave is about to use on the host.
type Claims struct {
	Ranges        []netip.Prefix // routed or assigned by tailweave: CGNAT, pool, transit
	DNS           netip.Addr     // address of the DNS service
	RulePref      int            // priority of tailweave's ip rule (Linux)
	Table         string         // tailweave's routing table (Linux)
	OwnInterfaces []string       // name prefixes of tailweave's own interfaces
	OwnAddr       netip.Addr     // interfaces carrying this address are tailweave's
}

// Snapshot is the host's network configuration and running processes.
type Snapshot struct {
	Routes     []Route
	Rules      []Rule
	Interfaces []Interface
	Processes  []string // command names
}

// Route is an IPv4 route.
type Route struct {
	Prefix netip.Prefix
	Dev    string // outgoing interface, if any
	Table  string // Linux routing table ("main", "local", "52", ...); empty elsewhere
	Type   string // Linux route type ("unicast", "throw", ...); empty elsewhere
}

// Rule is a Linux policy routing rule.
type Rule struct {
	Pref  int
	Iif   string
	Table string
}

// Interface is a network interface with its IPv4 addresses.
type Interface struct {
	Name  string
	Addrs []netip.Prefix // address with the length of its network
}

// maxListed is how many conflicts an error lists.
const maxListed = 5

// Check returns an error naming every conflict between the snapshot and the claims, or
// nil if there is none.
func Check(s Snapshot, c Claims) error {
	claims := slices.Clone(c.Ranges)
	if c.DNS.IsValid() {
		claims = append(claims, netip.PrefixFrom(c.DNS, c.DNS.BitLen()))
	}
	own := func(name string) bool {
		for _, p := range c.OwnInterfaces {
			if p != "" && strings.HasPrefix(name, p) {
				return true
			}
		}
		return false
	}
	ownAddr := map[string]bool{}
	for _, i := range s.Interfaces {
		for _, a := range i.Addrs {
			if c.OwnAddr.IsValid() && a.Addr() == c.OwnAddr {
				ownAddr[i.Name] = true
			}
		}
	}
	isOwn := func(name string) bool { return name != "" && (own(name) || ownAddr[name]) }

	// Conflicts with the same key are reported once, so a client with many peer routes
	// does not drown the message.
	var conflicts []string
	seen := map[string]bool{}
	add := func(key, format string, args ...any) {
		if !seen[key] {
			seen[key] = true
			conflicts = append(conflicts, fmt.Sprintf(format, args...))
		}
	}
	overlapping := func(p netip.Prefix) (netip.Prefix, bool) {
		for _, cl := range claims {
			if cl.Overlaps(p) {
				return cl, true
			}
		}
		return netip.Prefix{}, false
	}

	for _, i := range s.Interfaces {
		if isOwn(i.Name) {
			continue
		}
		for _, a := range i.Addrs {
			if cl, ok := overlapping(a.Masked()); ok {
				add(cl.String()+" "+i.Name, "address %s on %s overlaps %s", a, i.Name, cl)
			}
		}
	}
	for _, r := range s.Routes {
		// Default routes and full-tunnel halves (0.0.0.0/1, 128.0.0.0/1) cover everything;
		// tailweave's more specific routes win over them. Throw routes route nothing; local
		// and broadcast routes (Linux) repeat interface addresses, checked above.
		switch r.Type {
		case "throw", "local", "broadcast":
			continue
		}
		if r.Prefix.Bits() <= 1 || isOwn(r.Dev) || (c.Table != "" && r.Table == c.Table) {
			continue
		}
		cl, ok := overlapping(r.Prefix.Masked())
		if !ok {
			continue
		}
		via := "via " + r.Dev
		if r.Dev == "" {
			via = "as " + r.Type
		}
		if r.Table != "" && r.Table != "main" {
			via += " (table " + r.Table + ")"
		}
		key := cl.String() + " " + via
		if r.Prefix.Masked() == cl {
			add(key, "%s is already routed %s", cl, via)
		} else {
			add(key, "route %s %s overlaps %s", r.Prefix, via, cl)
		}
	}
	for _, r := range s.Rules {
		mine := r.Pref == c.RulePref && r.Iif == "lo" && r.Table == c.Table
		switch {
		case mine || c.RulePref == 0:
		case r.Pref == c.RulePref:
			add("pref", "ip rule priority %d is already used (lookup %s)", r.Pref, r.Table)
		case r.Table == c.Table:
			add("table", "routing table %s is already used by the ip rule at priority %d", r.Table, r.Pref)
		}
	}
	if len(conflicts) == 0 {
		return nil
	}

	var b strings.Builder
	b.WriteString("another program uses the network resources tailweave needs: ")
	listed := conflicts[:min(len(conflicts), maxListed)]
	b.WriteString(strings.Join(listed, "; "))
	if n := len(conflicts) - len(listed); n > 0 {
		fmt.Fprintf(&b, "; and %d more", n)
	}
	if tailscale(s) {
		b.WriteString(". Tailscale is running; quit it (or run `tailscale down`) before starting tailweave")
	} else {
		b.WriteString(". Stop the other VPN client or proxy before starting tailweave")
	}
	return fmt.Errorf("%s", b.String())
}

// tailscale reports whether the snapshot shows signs of a running Tailscale client.
func tailscale(s Snapshot) bool {
	for _, i := range s.Interfaces {
		if strings.HasPrefix(i.Name, "tailscale") {
			return true
		}
	}
	for _, p := range s.Processes {
		if p == "tailscaled" || p == "Tailscale" || p == "IPNExtension" || strings.HasPrefix(p, "io.tailscale.") {
			return true
		}
	}
	for _, r := range s.Rules {
		if (r.Pref >= 5210 && r.Pref <= 5270) || r.Table == "52" {
			return true
		}
	}
	for _, r := range s.Routes {
		if r.Table == "52" {
			return true
		}
	}
	return false
}

// Run takes a snapshot of this host and checks it against the claims. A snapshot that
// cannot be taken is logged, not fatal: the check is a courtesy, not a guarantee.
func Run(c Claims) error {
	s, err := Collect()
	if err != nil {
		slog.Warn("preflight check skipped", "err", err)
		return nil
	}
	return Check(s, c)
}
