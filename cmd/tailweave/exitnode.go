package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

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
	pick, ok, err := p.run(os.Stdin, os.Stdout, colorful(os.Stdout))
	if err != nil || !ok {
		return err
	}
	if pick == p.current() {
		fmt.Println(exitNodeLine(s.ExitNode))
		return nil
	}
	var ref weave.DeviceRef
	if pick > 0 {
		d := p.items[pick].dev
		ref = weave.DeviceRef{Tailnet: d.Tailnet, PeerID: d.PeerID}
	}
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

// picker is the list `tailweave exit-node` shows: no exit node, then every exit node on
// offer, in tailnet order.
type picker struct {
	items []pickItem
	cur   int // the highlighted item
	lines int // lines drawn last time, to redraw over them
}

type pickItem struct {
	dev    weave.Device // zero for "none"
	inUse  bool
	gone   bool // picked, but no longer offered
	online bool
}

func newPicker(s daemon.State) *picker {
	p := &picker{items: []pickItem{{inUse: s.ExitNode == nil, online: true}}}
	for _, d := range s.Devices {
		inUse := usedExit(s.ExitNode, d)
		if d.ExitNode || inUse {
			p.items = append(p.items, pickItem{dev: d, inUse: inUse, gone: !d.ExitNode, online: d.Online})
		}
	}
	p.cur = p.current()
	return p
}

// current returns the index of the item in use; 0 ("none") if the exit node in use is
// not in the list.
func (p *picker) current() int {
	for i, it := range p.items {
		if it.inUse {
			return i
		}
	}
	return 0
}

// list prints the items without the cursor, for output that is not a terminal.
func (p *picker) list(w io.Writer) {
	if len(p.items) == 1 {
		fmt.Fprintln(w, "no tailnet offers an exit node")
		return
	}
	for _, r := range p.rows(false) {
		fmt.Fprintln(w, r)
	}
}

// rows returns the lines of the list; cursor marks the highlighted item.
func (p *picker) rows(cursor bool) []string {
	var rs []row
	for i, it := range p.items {
		mark := "  "
		if cursor && i == p.cur {
			mark = "> "
		}
		r := row{{mark + "none", ""}, {"", ""}, {"", ""}, {"internet traffic leaves directly", ""}}
		if i > 0 {
			d := it.dev
			r = row{{mark + d.Name, ""}, {d.Tailnet, ""}, {ip(d.RealIP), ""}, {"", ""}}
			switch {
			case it.gone:
				r[3].text = "no longer offered"
			case !it.online:
				r[3].text = "offline"
			}
		}
		if it.inUse {
			r[3].text = strings.TrimPrefix(r[3].text+", in use", ", ")
		}
		rs = append(rs, r)
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

// key handles one key press; done reports that the user chose (ok) or gave up.
func (p *picker) key(k []byte) (done, ok bool) {
	switch string(k) {
	case "\x1b[A", "\x1bOA", "k":
		p.cur = (p.cur + len(p.items) - 1) % len(p.items)
	case "\x1b[B", "\x1bOB", "j", "\t":
		p.cur = (p.cur + 1) % len(p.items)
	case "\r", "\n":
		return true, true
	case "q", "\x1b", "\x03", "\x04": // Esc, Ctrl-C, Ctrl-D
		return true, false
	}
	return false, false
}

// draw writes the list to a terminal in raw mode, over the one drawn before.
func (p *picker) draw(w io.Writer, color bool) {
	var b strings.Builder
	if p.lines > 0 {
		fmt.Fprintf(&b, "\x1b[%dA", p.lines)
	}
	b.WriteString("\r\x1b[J")
	b.WriteString("exit node (↑/↓, enter to pick, q to cancel):\r\n")
	for i, r := range p.rows(true) {
		if color && i == p.cur {
			r = "\x1b[7m" + r + "\x1b[0m"
		}
		b.WriteString(r + "\r\n")
	}
	p.lines = len(p.items) + 1
	io.WriteString(w, b.String())
}

// run shows the list until the user picks an item or gives up.
func (p *picker) run(in *os.File, out io.Writer, color bool) (pick int, ok bool, err error) {
	old, err := term.MakeRaw(int(in.Fd()))
	if err != nil {
		return 0, false, err
	}
	defer term.Restore(int(in.Fd()), old)
	buf := make([]byte, 16)
	for {
		p.draw(out, color)
		n, err := in.Read(buf)
		if err != nil {
			return 0, false, err
		}
		if done, ok := p.key(buf[:n]); done {
			return p.cur, ok, nil
		}
	}
}
