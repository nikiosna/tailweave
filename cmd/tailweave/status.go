package main

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"strings"
	"text/tabwriter"

	"github.com/Nikiosna/tailweave/internal/client"
	"github.com/Nikiosna/tailweave/internal/daemon"
	"github.com/Nikiosna/tailweave/internal/weave"
)

// printStatus writes the daemon's state as tables.
func printStatus(w io.Writer, s daemon.State) {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintf(tw, "auto-mapping %s, DNS on %s, pool %s\n\n", onOff(s.AutoMap), s.DNS, s.Pool)

	fmt.Fprintln(tw, "TAILNET\tDOMAIN\tTHIS MACHINE\tIP\t")
	for _, t := range s.Tailnets {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", t.Name, or(t.Domain, "-"), or(t.Self, "-"), ip(t.IP), t.Error)
	}

	fmt.Fprintln(tw, "\nTAILNET\tDEVICE\tREAL IP\tREACHABLE AT\tMAPPING")
	for _, d := range s.Devices {
		name := d.Name
		if !d.Online {
			name += " (offline)"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", d.Tailnet, name, d.RealIP, routed(d), mapping(d))
	}
	tw.Flush()

	var open []weave.Conflict
	for _, c := range s.Conflicts {
		if !c.Resolved {
			open = append(open, c)
		}
	}
	if len(open) == 0 {
		fmt.Fprintln(w, "\nno unresolved conflicts")
		return
	}
	fmt.Fprintln(w, "\nunresolved conflicts:")
	for _, c := range open {
		var devs []string
		for _, d := range c.Devices {
			devs = append(devs, d.Name+" ("+d.Tailnet+")")
		}
		fmt.Fprintf(w, "  %s %s: %s: %s\n", c.Kind, c.Key, strings.Join(devs, ", "), c.Message)
	}
}

func routed(d weave.Device) string {
	if !d.RoutedIP.IsValid() {
		return "not routed"
	}
	return d.RoutedIP.String()
}

func mapping(d weave.Device) string {
	switch {
	case d.Mapping == nil:
		return "-"
	case d.Mapping.Auto:
		return "auto"
	default:
		return "manual"
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func or(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func ip(a netip.Addr) string {
	if !a.IsValid() {
		return "-"
	}
	return a.String()
}

func unresolved(s daemon.State) int {
	n := 0
	for _, c := range s.Conflicts {
		if !c.Resolved {
			n++
		}
	}
	return n
}

// findDevice looks a device up by name or peer ID in the daemon's current state.
func findDevice(ctx context.Context, c *client.Client, tailnet, device string) (weave.Device, error) {
	s, err := c.State(ctx)
	if err != nil {
		return weave.Device{}, err
	}
	return pickDevice(s, tailnet, device)
}

func pickDevice(s daemon.State, tailnet, device string) (weave.Device, error) {
	var found []weave.Device
	for _, d := range s.Devices {
		if d.Tailnet != tailnet {
			continue
		}
		if d.PeerID == device {
			return d, nil
		}
		if strings.EqualFold(d.Name, device) || strings.EqualFold(strings.TrimSuffix(d.DNSName, "."), device) {
			found = append(found, d)
		}
	}
	switch len(found) {
	case 0:
		return weave.Device{}, fmt.Errorf("no device %q in tailnet %q (see tailweave status)", device, tailnet)
	case 1:
		return found[0], nil
	default:
		return weave.Device{}, fmt.Errorf("%d devices named %q in tailnet %q, use the MagicDNS name", len(found), device, tailnet)
	}
}

func lookupDevice(s daemon.State, tailnet, peerID string) *weave.Device {
	for i := range s.Devices {
		if d := &s.Devices[i]; d.Tailnet == tailnet && d.PeerID == peerID {
			return d
		}
	}
	return nil
}
