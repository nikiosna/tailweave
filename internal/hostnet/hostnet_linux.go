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
	for _, args := range [][]string{
		{"addr", "add", netip.PrefixFrom(h.HostIP, 32).String(), "dev", h.Tun},
		{"addr", "add", netip.PrefixFrom(h.DNS, 32).String(), "dev", h.Tun},
		{"link", "set", h.Tun, "up"},
	} {
		if err := run("", "ip", args...); err != nil {
			return err
		}
	}
	if err := netcfg.AddRule(); err != nil {
		return err
	}
	for _, p := range h.Routes {
		if err := run("", "ip", "route", "replace", p.Masked().String(), "dev", h.Tun,
			"src", h.HostIP.String(), "table", fmt.Sprint(netcfg.Table)); err != nil {
			return err
		}
	}
	return nil
}

func (h *Host) setDomains(domains []string) error {
	return netcfg.RegisterDNS(h.Tun, h.DNS, domains)
}

// Teardown removes the rule and routes; the TUN's addresses go away with the device.
// Errors are ignored: parts may not exist.
func (h *Host) Teardown() {
	netcfg.DeleteRule()
	_ = run("", "ip", "route", "flush", "table", fmt.Sprint(netcfg.Table))
	if h.set {
		_ = run("", "resolvectl", "revert", h.Tun)
	}
	h.domains, h.set = nil, false
}
