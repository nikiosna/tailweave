package netcfg

import (
	"bytes"
	"fmt"
	"net/netip"
	"os/exec"
	"slices"
	"strings"

	"github.com/Nikiosna/tailweave/internal/weave"
)

// DNSLink is the dummy interface that carries the DNS service address on the host.
// Its name must not start with "tw-" (the lab matches those in iptables).
const DNSLink = "tailweave0"

// Host applies plans to this Linux machine. It needs root and the tools ip, nft and
// resolvectl. It re-applies only the parts that changed since the last call.
type Host struct {
	Namespaces []Namespace
	Blocked    []netip.Prefix // see HostRoutes
	DNSAddr    netip.Addr     // address of the DNS service, put on DNSLink

	routes  string
	rules   map[string]string
	domains []string
}

// Setup removes leftovers of an earlier run and creates the static parts: the ip rule,
// IP forwarding in the namespaces and the DNS link.
func (h *Host) Setup() error {
	h.Teardown()
	if err := run("", "ip", "rule", "add", "pref", fmt.Sprint(RulePref), "lookup", fmt.Sprint(Table)); err != nil {
		return err
	}
	for _, ns := range h.Namespaces {
		if err := run("", "ip", "netns", "exec", ns.Netns, "sysctl", "-qw", "net.ipv4.ip_forward=1"); err != nil {
			return err
		}
	}
	if h.DNSAddr.IsValid() {
		for _, args := range [][]string{
			{"link", "add", DNSLink, "type", "dummy"},
			{"addr", "add", netip.PrefixFrom(h.DNSAddr, h.DNSAddr.BitLen()).String(), "dev", DNSLink},
			{"link", "set", DNSLink, "up"},
		} {
			if err := run("", "ip", args...); err != nil {
				return err
			}
		}
	}
	return nil
}

// Apply brings routes, namespace rules and DNS registration in line with the plan.
func (h *Host) Apply(p weave.Plan) error {
	routes, err := HostRoutes(p, h.Namespaces, h.Blocked)
	if err != nil {
		return err
	}
	// Rules first, so a newly mapped IP is translated as soon as it is routed.
	if h.rules == nil {
		h.rules = map[string]string{}
	}
	for _, ns := range h.Namespaces {
		r := NamespaceRules(p, ns)
		if r == h.rules[ns.Netns] {
			continue
		}
		if err := run(r, "ip", "netns", "exec", ns.Netns, "nft", "-f", "-"); err != nil {
			return err
		}
		h.rules[ns.Netns] = r
	}
	if routes != h.routes {
		if err := run(routes, "ip", "-batch", "-"); err != nil {
			return err
		}
		h.routes = routes
	}
	if h.DNSAddr.IsValid() && !slices.Equal(p.Domains, h.domains) {
		if err := h.registerDNS(p.Domains); err != nil {
			return err
		}
		h.domains = slices.Clone(p.Domains)
	}
	return nil
}

// registerDNS makes systemd-resolved send queries for the tailnet domains to tailweave.
// Each domain is both a routing domain (~a.lab) and a search domain, so short host
// names work too.
func (h *Host) registerDNS(domains []string) error {
	if len(domains) == 0 {
		return run("", "resolvectl", "revert", DNSLink)
	}
	if err := run("", "resolvectl", "dns", DNSLink, h.DNSAddr.String()); err != nil {
		return err
	}
	args := []string{"domain", DNSLink}
	for _, d := range domains {
		args = append(args, "~"+d, d)
	}
	if err := run("", "resolvectl", args...); err != nil {
		return err
	}
	return run("", "resolvectl", "default-route", DNSLink, "false")
}

// Teardown removes everything tailweave configured. Errors are ignored: parts may not
// exist.
func (h *Host) Teardown() {
	for run("", "ip", "rule", "del", "pref", fmt.Sprint(RulePref)) == nil {
	}
	_ = run("", "ip", "route", "flush", "table", fmt.Sprint(Table))
	for _, ns := range h.Namespaces {
		_ = run("", "ip", "netns", "exec", ns.Netns, "nft", "delete", "table", "inet", nftTable)
	}
	_ = run("", "ip", "link", "del", DNSLink) // resolved forgets the link's settings with it
	h.routes, h.rules, h.domains = "", nil, nil
}

func run(stdin, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(stdin)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(out.String()))
	}
	return nil
}
