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

// Host applies plans to this Linux machine. It needs root and the tools ip, nft,
// iptables and resolvectl. It re-applies only the parts that changed since the last call.
// Namespaces are added and removed while it runs; the caller serializes all calls.
type Host struct {
	Namespaces []Namespace
	Blocked    []netip.Prefix // see HostRoutes
	DNSAddr    netip.Addr     // address of the DNS service, put on DNSLink
	Transit    netip.Prefix   // addresses of the namespaces' veth pairs, see HostRules

	routes  string
	rules   map[string]string
	domains []string
}

// dockerRules let the namespaces' traffic through Docker's FORWARD policy DROP.
var dockerRules = [][]string{
	{"-i", VethPrefix + "+", "-j", "ACCEPT"},
	{"-o", VethPrefix + "+", "-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"},
}

// Setup removes leftovers of an earlier run and creates the static parts: the ip rule,
// IP forwarding and NAT for the namespaces, and the DNS link.
func (h *Host) Setup() error {
	h.Teardown()
	// iif lo matches locally generated traffic only; forwarded traffic never sees Table.
	if err := run("", "ip", "rule", "add", "pref", fmt.Sprint(RulePref), "iif", "lo", "lookup", fmt.Sprint(Table)); err != nil {
		return err
	}
	if h.Transit.IsValid() {
		if err := run("", "sysctl", "-qw", "net.ipv4.ip_forward=1"); err != nil {
			return err
		}
		if err := run(HostRules(h.Transit), "nft", "-f", "-"); err != nil {
			return err
		}
		// Hosts running Docker drop forwarded traffic unless DOCKER-USER lets it through.
		if run("", "iptables", "-n", "-L", "DOCKER-USER") == nil {
			for _, r := range dockerRules {
				if err := run("", "iptables", append([]string{"-I", "DOCKER-USER"}, r...)...); err != nil {
					return err
				}
			}
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

// AddNamespace starts managing a tailnet's namespace; the next Apply configures it.
func (h *Host) AddNamespace(ns Namespace) {
	h.RemoveNamespace(ns.Tailnet)
	h.Namespaces = append(h.Namespaces, ns)
}

// RemoveNamespace stops managing a tailnet's namespace and removes tailweave's rules
// from it, if it still exists.
func (h *Host) RemoveNamespace(tailnet string) {
	h.Namespaces = slices.DeleteFunc(h.Namespaces, func(ns Namespace) bool {
		if ns.Tailnet != tailnet {
			return false
		}
		_ = run("", "ip", "netns", "exec", ns.Netns, "nft", "delete", "table", "inet", nftTable)
		delete(h.rules, ns.Netns)
		return true
	})
	h.routes = "" // routes into the namespace are gone with it
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
		if err := RegisterDNS(DNSLink, h.DNSAddr, p.Domains); err != nil {
			return err
		}
		h.domains = slices.Clone(p.Domains)
	}
	return nil
}

// RegisterDNS makes systemd-resolved send queries for the tailnet domains to addr, via
// link. Each domain is both a routing domain (~a.lab) and a search domain, so short host
// names work too. No domains revert the link's DNS settings.
func RegisterDNS(link string, addr netip.Addr, domains []string) error {
	if len(domains) == 0 {
		return run("", "resolvectl", "revert", link)
	}
	if err := run("", "resolvectl", "dns", link, addr.String()); err != nil {
		return err
	}
	args := []string{"domain", link}
	for _, d := range domains {
		args = append(args, "~"+d, d)
	}
	if err := run("", "resolvectl", args...); err != nil {
		return err
	}
	return run("", "resolvectl", "default-route", link, "false")
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
	_ = run("", "nft", "delete", "table", "ip", nftTable)
	for _, r := range dockerRules {
		for run("", "iptables", append([]string{"-D", "DOCKER-USER"}, r...)...) == nil {
		}
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
