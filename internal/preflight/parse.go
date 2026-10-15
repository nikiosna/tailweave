package preflight

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math/bits"
	"net/netip"
	"strconv"
	"strings"
)

// parseIPRoutes parses `ip -4 -j route show table all`.
func parseIPRoutes(b []byte) ([]Route, error) {
	var rs []struct {
		Type  string `json:"type"`
		Dst   string `json:"dst"`
		Dev   string `json:"dev"`
		Table string `json:"table"`
	}
	if err := json.Unmarshal(b, &rs); err != nil {
		return nil, fmt.Errorf("ip route: %w", err)
	}
	var out []Route
	for _, r := range rs {
		p, ok := parseDst(r.Dst)
		if !ok {
			continue
		}
		if r.Table == "" {
			r.Table = "main"
		}
		if r.Type == "" {
			r.Type = "unicast"
		}
		out = append(out, Route{Prefix: p, Dev: r.Dev, Table: r.Table, Type: r.Type})
	}
	return out, nil
}

// parseDst parses a route destination as ip prints it: "default", an address or a prefix.
func parseDst(s string) (netip.Prefix, bool) {
	if s == "default" {
		return netip.MustParsePrefix("0.0.0.0/0"), true
	}
	if p, err := netip.ParsePrefix(s); err == nil {
		return p, true
	}
	if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
		return netip.PrefixFrom(a, 32), true
	}
	return netip.Prefix{}, false
}

// parseIPRules parses `ip -4 -j rule`.
func parseIPRules(b []byte) ([]Rule, error) {
	var rs []struct {
		Priority int    `json:"priority"`
		Iif      string `json:"iif"`
		Table    string `json:"table"`
	}
	if err := json.Unmarshal(b, &rs); err != nil {
		return nil, fmt.Errorf("ip rule: %w", err)
	}
	out := make([]Rule, 0, len(rs))
	for _, r := range rs {
		out = append(out, Rule{Pref: r.Priority, Iif: r.Iif, Table: r.Table})
	}
	return out, nil
}

// parseIPAddrs parses `ip -4 -j addr`.
func parseIPAddrs(b []byte) ([]Interface, error) {
	var is []struct {
		Name  string `json:"ifname"`
		Addrs []struct {
			Family string `json:"family"`
			Local  string `json:"local"`
			Len    int    `json:"prefixlen"`
		} `json:"addr_info"`
	}
	if err := json.Unmarshal(b, &is); err != nil {
		return nil, fmt.Errorf("ip addr: %w", err)
	}
	var out []Interface
	for _, i := range is {
		iface := Interface{Name: i.Name}
		for _, a := range i.Addrs {
			ip, err := netip.ParseAddr(a.Local)
			if a.Family != "inet" || err != nil {
				continue
			}
			if p := netip.PrefixFrom(ip, a.Len); p.IsValid() {
				iface.Addrs = append(iface.Addrs, p)
			}
		}
		out = append(out, iface)
	}
	return out, nil
}

// parseNetstat parses `netstat -rn -f inet` as macOS prints it. Destinations leave out
// trailing zero octets ("100.64/10", "127", "192.168.1" for 192.168.1.0/24).
func parseNetstat(s string) []Route {
	var out []Route
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 {
			continue
		}
		if p, ok := parseNetstatDst(f[0]); ok {
			out = append(out, Route{Prefix: p, Dev: f[3]})
		}
	}
	return out
}

func parseNetstatDst(s string) (netip.Prefix, bool) {
	if s == "default" {
		return netip.MustParsePrefix("0.0.0.0/0"), true
	}
	addr, bitStr, hasBits := strings.Cut(s, "/")
	octets := strings.Split(addr, ".")
	if len(octets) > 4 {
		return netip.Prefix{}, false
	}
	n := 8 * len(octets)
	for len(octets) < 4 {
		octets = append(octets, "0")
	}
	a, err := netip.ParseAddr(strings.Join(octets, "."))
	if err != nil || !a.Is4() {
		return netip.Prefix{}, false
	}
	if hasBits {
		if n, err = strconv.Atoi(bitStr); err != nil {
			return netip.Prefix{}, false
		}
	}
	p, err := a.Prefix(n)
	return p, err == nil
}

// parseIfconfig parses the IPv4 addresses out of `ifconfig` on macOS.
func parseIfconfig(s string) []Interface {
	var out []Interface
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		l := sc.Text()
		if l == "" {
			continue
		}
		if l[0] != ' ' && l[0] != '\t' {
			if name, _, ok := strings.Cut(l, ":"); ok {
				out = append(out, Interface{Name: name})
			}
			continue
		}
		f := strings.Fields(l)
		if len(out) == 0 || len(f) < 2 || f[0] != "inet" {
			continue
		}
		a, err := netip.ParseAddr(f[1])
		if err != nil {
			continue
		}
		n := 32
		for i := 2; i+1 < len(f); i++ {
			if f[i] == "netmask" {
				if m, err := strconv.ParseUint(strings.TrimPrefix(f[i+1], "0x"), 16, 32); err == nil {
					n = bits.OnesCount32(uint32(m))
				}
			}
		}
		last := &out[len(out)-1]
		last.Addrs = append(last.Addrs, netip.PrefixFrom(a, n))
	}
	return out
}
