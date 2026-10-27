package main

import (
	"cmp"
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/Nikiosna/tailweave/internal/client"
	"github.com/Nikiosna/tailweave/internal/daemon"
	"github.com/Nikiosna/tailweave/internal/weave"
	"golang.org/x/term"
)

// exitNode lets the user pick the exit node from a list in the terminal. Without a
// terminal, it prints the exit nodes on offer.
func exitNode(ctx context.Context, args []string) error {
	fs, socket := flags("exit-node")
	if err := parse(fs, args); err != nil {
		return err
	}
	c := client.New(*socket)
	s, err := c.State(ctx)
	if err != nil {
		return err
	}
	p := newPicker(s)
	in, out := int(os.Stdin.Fd()), int(os.Stdout.Fd())
	if !term.IsTerminal(in) || !term.IsTerminal(out) {
		p.list(os.Stdout)
		return nil
	}
	pick, err := p.run(os.Stdin, os.Stdout, colorful(os.Stdout))
	if err != nil || pick == nil {
		return err
	}
	if pick.inUse {
		fmt.Println(exitNodeLine(s.ExitNode))
		return nil
	}
	ref := weave.DeviceRef{Tailnet: pick.dev.Tailnet, PeerID: pick.dev.PeerID} // zero for "none"
	s, err = c.SetExitNode(ctx, ref.Tailnet, ref.PeerID)
	if err != nil {
		return err
	}
	fmt.Println(exitNodeLine(s.ExitNode))
	return nil
}

// setExitNode handles `set -exit-node=<tailnet>/<device>`; an empty value turns the exit
// node off.
func setExitNode(ctx context.Context, c *client.Client, v string) error {
	var tailnet, peerID string
	if v != "" {
		tn, name, ok := strings.Cut(v, "/")
		if !ok || tn == "" || name == "" {
			return fmt.Errorf("-exit-node wants <tailnet>/<device>, e.g. a/exit-a, or nothing to turn it off")
		}
		d, err := findDevice(ctx, c, tn, name)
		if err != nil {
			return err
		}
		tailnet, peerID = d.Tailnet, d.PeerID
	}
	s, err := c.SetExitNode(ctx, tailnet, peerID)
	if err != nil {
		return err
	}
	fmt.Println(exitNodeLine(s.ExitNode))
	return nil
}

// exitNodeLine tells where this machine's internet traffic goes.
func exitNodeLine(e *weave.Exit) string {
	switch {
	case e == nil:
		return "no exit node; internet traffic leaves this machine directly"
	case !e.Available:
		return fmt.Sprintf("exit node %s (%s) is not available; internet traffic is blocked until it is back", e.Name, e.Tailnet)
	default:
		return fmt.Sprintf("internet traffic goes through exit node %s (%s), %s", e.Name, e.Tailnet, e.IP)
	}
}

// picker is the tree `tailweave exit-node` shows: no exit node, the exit nodes without a
// location in tailnet order, then those with one (Mullvad's, for example) by country and
// city. Countries and cities start closed, except around the exit node in use; picking a
// city picks its best exit node.
type picker struct {
	nodes  []*pickNode // the top level
	cur    int         // the highlighted line among the visible ones
	top    int         // the first line shown when the list is taller than the terminal
	height int         // lines the list may take; 0 for no limit
	width  int         // columns of the terminal; 0 for no limit
	lines  int         // lines drawn last time, to redraw over them
}

type pickItem struct {
	dev    weave.Device // zero for "none"
	inUse  bool
	gone   bool // picked, but no longer offered
	online bool
}

// pickNode is one line of the picker: "none", an exit node, a country, or a city. A city
// with one exit node is that exit node.
type pickNode struct {
	label string    // country or city; empty for "none" and exit nodes
	item  *pickItem // the exit node; for a city its best one; nil for a country
	kids  []*pickNode
	open  bool
	depth int
}

func (n *pickNode) group() bool { return len(n.kids) > 0 }

// items returns the exit nodes in n, n's own for a line without children.
func (n *pickNode) items() []*pickItem {
	if !n.group() {
		return []*pickItem{n.item}
	}
	var its []*pickItem
	for _, k := range n.kids {
		its = append(its, k.items()...)
	}
	return its
}

func (n *pickNode) inUse() bool {
	return slices.ContainsFunc(n.items(), func(it *pickItem) bool { return it.inUse })
}

func newPicker(s daemon.State) *picker {
	p := &picker{nodes: []*pickNode{{item: &pickItem{inUse: s.ExitNode == nil, online: true}}}}
	var located []*pickItem
	for _, d := range s.Devices {
		inUse := usedExit(s.ExitNode, d)
		if !d.ExitNode && !inUse {
			continue
		}
		it := &pickItem{dev: d, inUse: inUse, gone: !d.ExitNode, online: d.Online}
		if d.Location != nil && d.Location.Country != "" {
			located = append(located, it)
		} else {
			p.nodes = append(p.nodes, &pickNode{item: it})
		}
	}
	p.nodes = append(p.nodes, byPlace(located)...)
	// Open the way to the exit node in use and start there.
	for _, n := range p.nodes {
		n.open = n.group() && n.inUse()
		for _, k := range n.kids {
			k.open = k.group() && k.inUse()
		}
	}
	vis := p.visible()
	p.cur = max(0, slices.IndexFunc(vis, func(n *pickNode) bool {
		return n.item != nil && n.item.inUse && (!n.group() || !n.open)
	}))
	return p
}

// byPlace groups exit nodes by tailnet and country, then by city, sorted by name.
func byPlace(items []*pickItem) []*pickNode {
	slices.SortStableFunc(items, func(a, b *pickItem) int {
		la, lb := a.dev.Location, b.dev.Location
		return cmp.Or(cmp.Compare(la.Country, lb.Country), cmp.Compare(a.dev.Tailnet, b.dev.Tailnet),
			cmp.Compare(la.City, lb.City), cmp.Compare(la.CityCode, lb.CityCode), cmp.Compare(a.dev.Name, b.dev.Name))
	})
	var countries []*pickNode
	var country, city *pickNode
	for i, it := range items {
		l := it.dev.Location
		if i == 0 || l.Country != items[i-1].dev.Location.Country || it.dev.Tailnet != items[i-1].dev.Tailnet {
			country = &pickNode{label: l.Country}
			countries = append(countries, country)
			city = nil
		}
		if city == nil || l.City != city.kids[0].item.dev.Location.City || l.CityCode != city.kids[0].item.dev.Location.CityCode {
			city = &pickNode{label: or(l.City, "-"), depth: 1}
			country.kids = append(country.kids, city)
		}
		city.kids = append(city.kids, &pickNode{item: it, depth: 2})
	}
	for _, c := range countries {
		for _, ci := range c.kids {
			if len(ci.kids) == 1 {
				ci.item, ci.kids = ci.kids[0].item, nil
				continue
			}
			ci.item = best(ci.items())
		}
	}
	return countries
}

// best returns the exit node to use from several in one place: online first, then the
// one with the highest priority, as Tailscale picks it.
func best(items []*pickItem) *pickItem {
	down := func(it *pickItem) int {
		if it.online && !it.gone {
			return 0
		}
		return 1
	}
	return slices.MinFunc(items, func(a, b *pickItem) int {
		return cmp.Or(cmp.Compare(down(a), down(b)),
			-cmp.Compare(a.dev.Location.Priority, b.dev.Location.Priority), cmp.Compare(a.dev.Name, b.dev.Name))
	})
}

// visible returns the lines of the open part of the tree, in order.
func (p *picker) visible() []*pickNode {
	var vis []*pickNode
	var walk func([]*pickNode)
	walk = func(ns []*pickNode) {
		for _, n := range ns {
			vis = append(vis, n)
			if n.open {
				walk(n.kids)
			}
		}
	}
	walk(p.nodes)
	return vis
}

// list prints the whole tree without the cursor, for output that is not a terminal.
func (p *picker) list(w io.Writer) {
	if len(p.nodes) == 1 {
		fmt.Fprintln(w, "no tailnet offers an exit node")
		return
	}
	var open func([]*pickNode)
	open = func(ns []*pickNode) {
		for _, n := range ns {
			n.open = n.group()
			open(n.kids)
		}
	}
	open(p.nodes)
	for _, r := range p.rows(p.visible(), false) {
		fmt.Fprintln(w, r)
	}
}

// rows returns the given lines as text; cursor marks the highlighted one.
func (p *picker) rows(vis []*pickNode, cursor bool) []string {
	var rs []row
	for i, n := range vis {
		mark := "  "
		if cursor && i == p.cur {
			mark = "> "
		}
		mark += strings.Repeat("  ", n.depth)
		switch {
		case n.open:
			mark += "▾ "
		case n.group():
			mark += "▸ "
		case n.depth > 0:
			mark += "  "
		}
		rs = append(rs, n.row(mark))
	}
	w := widths(rs)
	var out []string
	for _, r := range rs {
		var b strings.Builder
		printer{&b, false}.row("", r, w)
		out = append(out, strings.TrimRight(b.String(), "\n"))
	}
	return out
}

// row is the line of n: name, tailnet, IP and a note.
func (n *pickNode) row(mark string) row {
	it := n.item
	switch {
	case it == nil: // country
		r := row{{mark + n.label, ""}, {n.items()[0].dev.Tailnet, ""}, {"", ""}, {count(len(n.items()), "exit node"), ""}}
		if n.inUse() && !n.open {
			r[3].text += ", in use"
		}
		return r
	case it.dev.Tailnet == "":
		r := row{{mark + "none", ""}, {"", ""}, {"", ""}, {"internet traffic leaves directly", ""}}
		if it.inUse {
			r[3].text += ", in use"
		}
		return r
	}
	name, note := it.dev.Name, it.note()
	if n.label != "" { // city
		name = n.label
		note = strings.TrimSuffix(it.dev.Name+", "+note, ", ")
		if n.group() {
			note = fmt.Sprintf("%s (best of %d)", it.dev.Name, len(n.kids))
			if !it.online || it.gone {
				note += ", offline"
			}
			if n.inUse() && !n.open {
				note += ", in use"
			}
		}
	}
	return row{{mark + name, ""}, {it.dev.Tailnet, ""}, {ip(it.dev.RealIP), ""}, {note, ""}}
}

// note says what is special about an exit node.
func (it *pickItem) note() string {
	var notes []string
	switch {
	case it.gone:
		notes = append(notes, "no longer offered")
	case !it.online:
		notes = append(notes, "offline")
	}
	if it.inUse {
		notes = append(notes, "in use")
	}
	return strings.Join(notes, ", ")
}

func count(n int, what string) string {
	if n == 1 {
		return "1 " + what
	}
	return fmt.Sprintf("%d %ss", n, what)
}

// key handles one key press; done reports that the user chose (pick) or gave up (nil).
func (p *picker) key(k []byte) (done bool, pick *pickItem) {
	vis := p.visible()
	n := vis[p.cur]
	page := max(1, p.height-1)
	switch string(k) {
	case "\x1b[A", "\x1bOA", "k":
		p.cur = (p.cur + len(vis) - 1) % len(vis)
	case "\x1b[B", "\x1bOB", "j", "\t":
		p.cur = (p.cur + 1) % len(vis)
	case "\x1b[5~": // page up
		p.cur = max(0, p.cur-page)
	case "\x1b[6~": // page down
		p.cur = min(len(vis)-1, p.cur+page)
	case "\x1b[H", "\x1bOH", "g":
		p.cur = 0
	case "\x1b[F", "\x1bOF", "G":
		p.cur = len(vis) - 1
	case "\x1b[C", "\x1bOC", "l", "+":
		n.open = n.group()
	case "\x1b[D", "\x1bOD", "h", "-":
		if n.open {
			n.open = false
			break
		}
		// To the parent: the closest line above with less depth.
		for i := p.cur - 1; i >= 0 && n.depth > 0; i-- {
			if vis[i].depth < n.depth {
				p.cur = i
				break
			}
		}
	case "\r", "\n", " ":
		if n.item == nil { // a country only opens and closes
			n.open = !n.open
			break
		}
		return true, n.item
	case "q", "\x1b", "\x03", "\x04": // Esc, Ctrl-C, Ctrl-D
		return true, nil
	}
	return false, nil
}

// draw writes the list to a terminal in raw mode, over the one drawn before. A list
// taller than the terminal scrolls with the cursor.
func (p *picker) draw(w io.Writer, color bool) {
	vis := p.visible()
	p.cur = min(p.cur, len(vis)-1)
	n := len(vis)
	if p.height > 0 {
		n = min(n, p.height)
	}
	p.top = min(max(p.top, p.cur-n+1), p.cur, len(vis)-n)

	var b strings.Builder
	if p.lines > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", p.lines)
	}
	b.WriteString("\r\x1b[J")
	head := "exit node (↑/↓, enter to pick, q to cancel)"
	if slices.ContainsFunc(p.nodes, (*pickNode).group) {
		head = "exit node (↑/↓, →/← to open/close, enter to pick, q to cancel)"
	}
	if n < len(vis) {
		head += fmt.Sprintf(" %d/%d", p.cur+1, len(vis))
	}
	b.WriteString(p.clip(head+":") + "\r\n")
	rows := p.rows(vis, true)
	for i := p.top; i < p.top+n; i++ {
		r := p.clip(rows[i])
		if color && i == p.cur {
			r = "\x1b[7m" + r + "\x1b[0m"
		}
		b.WriteString(r + "\r\n")
	}
	p.lines = n + 1
	io.WriteString(w, b.String())
}

// clip cuts s to the terminal width, so no line wraps and throws off the redraw.
func (p *picker) clip(s string) string {
	if p.width <= 0 || utf8.RuneCountInString(s) < p.width {
		return s
	}
	return string([]rune(s)[:p.width-1])
}

// run shows the list until the user picks an exit node (or "none") or gives up (nil).
func (p *picker) run(in *os.File, out *os.File, color bool) (*pickItem, error) {
	old, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return nil, err
	}
	defer term.Restore(int(in.Fd()), old)
	buf := make([]byte, 16)
	for {
		if w, h, err := term.GetSize(int(out.Fd())); err == nil {
			p.width, p.height = w, max(1, h-2) // the head line, and one to spare
		}
		p.draw(out, color)
		n, err := in.Read(buf)
		if err != nil {
			return nil, err
		}
		if done, pick := p.key(buf[:n]); done {
			return pick, nil
		}
	}
}
