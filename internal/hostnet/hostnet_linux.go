package hostnet

import (
	"fmt"
	"net/netip"

	"github.com/Nikiosna/tailweave/internal/netcfg"
)

// TunName is the name tailweave asks for when it creates its TUN device.
const TunName = "tailweave0"

// Cleanup removes what an earlier run of either backend left behind, and nothing else.
func Cleanup(dns netip.Addr) {
	(&netcfg.Host{}).Teardown()
}

// Setup gives the TUN its addresses and routes the tailnet ranges into it through
// netcfg.Table, which only locally generated traffic consults.
func (h *Host) Setup() error {
	cmds := [][]string{
		{"addr", "add", netip.PrefixFrom(h.HostIP, 32).String(), "dev", h.Tun},
		{"addr", "add", netip.PrefixFrom(h.DNS, 32).String(), "dev", h.Tun},
	}
	if h.HostIP6.IsValid() {
		cmds = append(cmds, []string{"-6", "addr", "add", netip.PrefixFrom(h.HostIP6, 128).String(), "dev", h.Tun, "nodad"})
	}
	for _, args := range append(cmds, []string{"link", "set", h.Tun, "up"}) {
		if err := run("", "ip", args...); err != nil {
			return err
		}
	}
	if err := netcfg.AddRule(h.HostIP6.IsValid()); err != nil {
		return err
	}
	for _, p := range h.Routes {
		if err := h.addRoute(p); err != nil {
			return err
		}
	}
	return nil
}

func (h *Host) addRoute(p netip.Prefix) error {
	src := h.srcFor(p)
	if !src.IsValid() {
		return nil // no IPv6 on this host
	}
	return run("", "ip", "route", "replace", p.Masked().String(), "dev", h.Tun,
		"src", src.String(), "table", fmt.Sprint(netcfg.Table))
}

func (h *Host) delRoute(p netip.Prefix) {
	_ = run("", "ip", "route", "del", p.Masked().String(), "table", fmt.Sprint(netcfg.Table))
}

func (h *Host) setDomains(domains []string) error {
	return netcfg.RegisterDNS(h.Tun, h.DNS, domains)
}

// Teardown removes the rule and routes; the TUN's addresses go away with the device.
// Errors are ignored: parts may not exist.
func (h *Host) Teardown() {
	netcfg.DeleteRule()
	_ = run("", "ip", "route", "flush", "table", fmt.Sprint(netcfg.Table))
	_ = run("", "ip", "-6", "route", "flush", "table", fmt.Sprint(netcfg.Table))
	h.extra = nil
	if h.set {
		_ = run("", "resolvectl", "revert", h.Tun)
	}
	h.domains, h.set = nil, false
}
