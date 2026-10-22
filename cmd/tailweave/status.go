package main

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Nikiosna/tailweave/internal/client"
	"github.com/Nikiosna/tailweave/internal/daemon"
	"github.com/Nikiosna/tailweave/internal/weave"
)

// statusOptions select what printStatus shows.
type statusOptions struct {
	all     bool   // also offline devices that are routed at their real IP
	tailnet string // only this tailnet, if set
	color   bool   // ANSI colors
}

// ANSI styles.
const (
	bold   = "1"
	dim    = "2"
	red    = "31"
	yellow = "33"
)

// printStatus writes the daemon's state, grouped by tailnet. Devices show their real
// IP only where it differs from the one they are reachable at; offline devices without
// a mapping are only counted unless o.all is set.
func printStatus(w io.Writer, s daemon.State, o statusOptions) {
	p := printer{w, o.color}
	if s.Stopped {
		p.line(red, "tailweave is stopped; `tailweave start` switches it on again")
	}
	open := openConflicts(s, o.tailnet)
	head := fmt.Sprintf("auto-mapping %s, DNS on %s, pool %s", onOff(s.AutoMap), s.DNS, s.Pool)
	if s.Operator != nil {
		head += ", operator " + s.Operator.Name
	}
	switch len(open) {
	case 0:
		head += ", no conflicts"
	case 1:
		head += ", 1 unresolved conflict"
	default:
		head += fmt.Sprintf(", %d unresolved conflicts", len(open))
	}
	fmt.Fprintln(w, head)
	for _, c := range open {
		var devs []string
		for _, d := range c.Devices {
			devs = append(devs, d.Name+" ("+d.Tailnet+")")
		}
		p.line(red, "  %s %s: %s: %s", c.Kind, c.Key, strings.Join(devs, ", "), c.Message)
	}
	if len(s.Tailnets) == 0 {
		fmt.Fprintln(w, "\nno tailnets yet, add one with `tailweave login <name>`")
		return
	}

	type group struct {
		head    row
		devices []row
		hidden  int
	}
	var groups []group
	var heads, devices []row
	for _, t := range s.Tailnets {
		if o.tailnet != "" && t.Name != o.tailnet {
			continue
		}
		var g group
		total, online := 0, 0
		for _, d := range s.Devices {
			if d.Tailnet != t.Name {
				continue
			}
			total++
			if d.Online {
				online++
			} else if !o.all && d.Mapping == nil && d.RoutedIP == d.RealIP {
				g.hidden++
				continue
			}
			g.devices = append(g.devices, deviceRow(d, open))
			for _, r := range d.Routes {
				g.devices = append(g.devices, routeRow(d, r, open))
			}
		}
		state := cell{or(t.State, "-"), ""}
		if t.State != "Running" {
			state.style = yellow
		}
		var note cell
		switch {
		case t.AuthURL != "":
			note = cell{"log in at " + t.AuthURL, yellow}
		case t.LoginError != "" || t.Error != "":
			note = cell{or(t.LoginError, t.Error), red}
		case total > 0:
			note = cell{fmt.Sprintf("%d of %d online", online, total), dim}
		}
		g.head = row{{t.Name, bold}, state, {or(t.Domain, "-"), ""}, {or(t.Self, "-"), ""}, {ip(t.IP), ""}, note}
		groups = append(groups, g)
		heads = append(heads, g.head)
		devices = append(devices, g.devices...)
	}
	hw, dw := widths(heads), widths(devices)
	for _, g := range groups {
		fmt.Fprintln(w)
		p.row("", g.head, hw)
		for _, r := range g.devices {
			p.row("  ", r, dw)
		}
		if g.hidden > 0 {
			p.line(dim, "  … %d offline, show with -a", g.hidden)
		}
	}
}

// deviceRow shows a device's name, the IP it is reachable at and what is special about it.
func deviceRow(d weave.Device, open []weave.Conflict) row {
	name, addr := cell{d.Name, ""}, cell{d.RealIP.String(), ""}
	var notes []string
	style := ""
	switch {
	case !d.RoutedIP.IsValid():
		addr.style, style = red, red
		notes = append(notes, notRouted(d, open))
	case d.RoutedIP != d.RealIP:
		addr = cell{d.RoutedIP.String() + " ← " + d.RealIP.String(), yellow}
		if d.Mapping != nil {
			notes = append(notes, mapping(d))
		}
	}
	if !d.Online {
		name.style = dim
		if addr.style == "" {
			addr.style = dim
		}
		notes = append(notes, "offline")
		if style == "" {
			style = dim
		}
	}
	return row{name, addr, {strings.Join(notes, ", "), style}}
}

// routeRow shows a subnet route of a device below it: the prefix it is reachable at and
// what is special about it.
func routeRow(d weave.Device, r weave.Route, open []weave.Conflict) row {
	addr, note := cell{r.Prefix.String(), ""}, cell{"route", ""}
	switch {
	case !r.Routed.IsValid():
		addr.style, note.style = red, red
		note.text += ", " + routeNotRouted(d, r, open)
	case r.Routed != r.Prefix:
		addr = cell{r.Routed.String() + " ← " + r.Prefix.String(), yellow}
		if r.Mapping != nil && r.Mapping.Auto {
			note.text += ", auto"
		} else if r.Mapping != nil {
			note.text += ", manual"
		}
	}
	if !d.Online && addr.style == "" {
		addr.style, note.style = dim, dim
	}
	return row{{"", ""}, addr, note}
}

// routeNotRouted explains why a subnet route is not reachable.
func routeNotRouted(d weave.Device, r weave.Route, open []weave.Conflict) string {
	for _, c := range open {
		key, err := netip.ParsePrefix(c.Key)
		if err != nil || !key.Overlaps(r.Prefix) ||
			!slices.ContainsFunc(c.Devices, func(x weave.DeviceRef) bool { return x.Tailnet == d.Tailnet && x.PeerID == d.PeerID }) {
			continue
		}
		var others []string
		for _, x := range c.Devices {
			if x.Tailnet != d.Tailnet {
				others = append(others, x.Name+" ("+x.Tailnet+")")
			}
		}
		if c.Kind == weave.ConflictRoute && len(others) > 0 {
			return "not routed, overlaps a route of " + strings.Join(others, ", ")
		}
		if strings.HasPrefix(c.Message, "not routed") {
			return c.Message
		}
		return "not routed: " + c.Message
	}
	return "not routed"
}

// notRouted explains why a device is not reachable, from the conflict it is part of.
func notRouted(d weave.Device, open []weave.Conflict) string {
	for _, c := range open {
		var others []string
		found := false
		for _, r := range c.Devices {
			if r.Tailnet == d.Tailnet && r.PeerID == d.PeerID {
				found = true
			} else {
				others = append(others, r.Name+" ("+r.Tailnet+")")
			}
		}
		switch {
		case !found:
		case c.Kind == weave.ConflictIP && len(others) > 0:
			return "not routed, same IP as " + strings.Join(others, ", ")
		case c.Message != "":
			return "not routed: " + c.Message
		}
	}
	return "not routed"
}

// openConflicts returns the unresolved conflicts, only those involving tailnet if set.
func openConflicts(s daemon.State, tailnet string) []weave.Conflict {
	var open []weave.Conflict
	for _, c := range s.Conflicts {
		if c.Resolved {
			continue
		}
		if tailnet != "" && !slices.ContainsFunc(c.Devices, func(d weave.DeviceRef) bool { return d.Tailnet == tailnet }) {
			continue
		}
		open = append(open, c)
	}
	return open
}

type cell struct{ text, style string }

type row []cell

// widths returns the width of each column but the last, which needs no padding.
func widths(rows []row) []int {
	var w []int
	for _, r := range rows {
		for i, c := range r[:len(r)-1] {
			if i == len(w) {
				w = append(w, 0)
			}
			w[i] = max(w[i], utf8.RuneCountInString(c.text))
		}
	}
	return w
}

// printer writes aligned rows, colored if wanted. It pads by hand because tabwriter
// would count the escape sequences as text.
type printer struct {
	w     io.Writer
	color bool
}

func (p printer) paint(style, s string) string {
	if !p.color || style == "" || s == "" {
		return s
	}
	return "\x1b[" + style + "m" + s + "\x1b[0m"
}

func (p printer) row(indent string, r row, w []int) {
	var b strings.Builder
	b.WriteString(indent)
	for i, c := range r {
		if i < len(r)-1 {
			pad := w[i] - utf8.RuneCountInString(c.text) + 2
			b.WriteString(p.paint(c.style, c.text) + strings.Repeat(" ", pad))
		} else {
			b.WriteString(p.paint(c.style, c.text))
		}
	}
	fmt.Fprintln(p.w, strings.TrimRight(b.String(), " "))
}

func (p printer) line(style, format string, args ...any) {
	fmt.Fprintln(p.w, p.paint(style, fmt.Sprintf(format, args...)))
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
	return len(openConflicts(s, ""))
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

// printRoute tells where a tailnet's subnet route is reachable now.
func printRoute(s daemon.State, tailnet string, route netip.Prefix) {
	for _, d := range s.Devices {
		for _, r := range d.Routes {
			if d.Tailnet == tailnet && r.Prefix == route.Masked() {
				at := "not routed"
				if r.Routed.IsValid() {
					at = r.Routed.String()
				}
				fmt.Printf("route %s of %s (%s) is reachable at %s\n", r.Prefix, d.Name, tailnet, at)
				return
			}
		}
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
