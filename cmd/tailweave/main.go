// Command tailweave joins one machine to several tailnets at once.
//
// `tailweave run` is the daemon: it runs one Tailscale instance per tailnet, isolated
// from the others (a tailscaled per network namespace, or a tsnet engine behind a
// userspace router), and merges the tailnets into the host: routes, collision handling
// and DNS.
// The other commands control it through its Unix socket; `tailweave login` adds tailnets.
// `tailweave ui` adds an optional web UI on top. See AGENTS.md for the roadmap.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"os/user"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Nikiosna/tailweave/internal/client"
	"github.com/Nikiosna/tailweave/internal/daemon"
	"github.com/Nikiosna/tailweave/internal/ui"
)

var version = "dev"

const usage = `usage: tailweave <command> [flags]

commands:
  run [-config <file>]               the daemon: merge all tailnets into this machine
  login <tailnet> [flags]            add a tailnet or log in again: via a web link, or
                                     with -auth-key <key>|file:<path>|- (see login -h)
  logout <tailnet>                   log out; the tailnet stays known
  remove <tailnet>                   log out and forget the tailnet
  status [<tailnet>] [-a] [-json]    tailnets, devices and conflicts; -a also lists
                                     offline devices
  set -auto-map=<true|false>         remap colliding devices automatically or not
  set -operator=<user>               let a user change tailweave without sudo
                                     (root only; -operator= removes it)
  map <tailnet> <device> [<ip>]      route a device at an IP (default: a free pool address)
  map <tailnet> <route> [<prefix>]   route a subnet route, e.g. 10.0.0.0/24, at a prefix
                                     of the same size (default: a free pool block)
  unmap <tailnet> <device>|<route>   remove a device's or a route's mapping
  ui [-listen <addr>]                serve a web UI for the running daemon
  version                            print the version

run needs root. Every user may run status; the other commands need root or the
operator. The commands talking to the daemon take -socket <path> (default
` + daemon.DefaultSocket + `).
`

// errUsage makes main print the usage and exit with status 2.
var errUsage = errors.New("usage")

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "run":
		err = run(ctx, args)
	case "login":
		err = login(ctx, args)
	case "logout":
		err = logout(ctx, args)
	case "remove":
		err = remove(ctx, args)
	case "status":
		err = status(ctx, args)
	case "set":
		err = set(ctx, args)
	case "map":
		err = mapDevice(ctx, args)
	case "unmap":
		err = unmapDevice(ctx, args)
	case "ui":
		err = serveUI(ctx, args)
	case "version":
		fmt.Println(version)
	default:
		err = errUsage
	}
	switch {
	case errors.Is(err, errUsage):
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	case err != nil:
		fmt.Fprintf(os.Stderr, "tailweave %s: %v\n", cmd, err)
		os.Exit(1)
	}
}

func newFlagSet(cmd string) *flag.FlagSet {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage of tailweave %s:\n", cmd)
		fs.PrintDefaults()
	}
	return fs
}

// flags returns a flag set for cmd with the -socket flag every daemon command has.
func flags(cmd string) (*flag.FlagSet, *string) {
	fs := newFlagSet(cmd)
	return fs, fs.String("socket", daemon.DefaultSocket, "Unix socket of the daemon")
}

// parse parses the flags, which may also follow the arguments (`login a -auth-key ...`),
// and checks that the number of arguments is one of nargs (default: none).
func parse(fs *flag.FlagSet, args []string, nargs ...int) error {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				os.Exit(0)
			}
			os.Exit(2) // the flag package has printed the problem and the usage
		}
		if fs.NArg() == 0 {
			break
		}
		pos, args = append(pos, fs.Arg(0)), fs.Args()[1:]
	}
	_ = fs.Parse(append([]string{"--"}, pos...)) // makes fs.Arg see the arguments
	if len(nargs) == 0 {
		nargs = []int{0}
	}
	if !slices.Contains(nargs, fs.NArg()) {
		return errUsage
	}
	return nil
}

func run(ctx context.Context, args []string) error {
	fs := newFlagSet("run")
	config := fs.String("config", "/etc/tailweave/config.json", "config file (optional)")
	if err := parse(fs, args); err != nil {
		return err
	}
	cfg, err := daemon.LoadConfig(*config)
	if err != nil {
		return err
	}
	d, err := daemon.New(cfg)
	if err != nil {
		return err
	}
	return d.Run(ctx)
}

func status(ctx context.Context, args []string) error {
	fs, socket := flags("status")
	asJSON := fs.Bool("json", false, "print the daemon's state as JSON")
	all := fs.Bool("a", false, "also list offline devices that need no attention")
	if err := parse(fs, args, 0, 1); err != nil {
		return err
	}
	s, err := client.New(*socket).State(ctx)
	if err != nil {
		return err
	}
	o := statusOptions{all: *all, tailnet: fs.Arg(0), color: colorful(os.Stdout)}
	if o.tailnet != "" && !slices.ContainsFunc(s.Tailnets, func(t daemon.TailnetState) bool { return t.Name == o.tailnet }) {
		return fmt.Errorf("no tailnet %q (see tailweave status)", o.tailnet)
	}
	if *asJSON {
		e := json.NewEncoder(os.Stdout)
		e.SetIndent("", "  ")
		return e.Encode(s)
	}
	printStatus(os.Stdout, s, o)
	return nil
}

// colorful reports whether f is a terminal that wants colors (see no-color.org).
func colorful(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func set(ctx context.Context, args []string) error {
	fs, socket := flags("set")
	var autoMap *bool
	fs.Func("auto-map", "remap colliding devices automatically (true|false)", func(v string) error {
		b, err := strconv.ParseBool(v)
		autoMap = &b
		return err
	})
	var operator *string
	fs.Func("operator", "user who may change tailweave without sudo (empty: nobody)", func(v string) error {
		operator = &v
		return nil
	})
	if err := parse(fs, args); err != nil {
		return err
	}
	if autoMap == nil && operator == nil {
		return errUsage
	}
	c := client.New(*socket)
	if operator != nil {
		op := daemon.Operator{Name: *operator}
		if op.Name != "" {
			uid, err := lookupUID(op.Name)
			if err != nil {
				return err
			}
			op.UID = uid
		}
		s, err := c.SetOperator(ctx, op)
		if err != nil {
			return err
		}
		if s.Operator == nil {
			fmt.Println("no operator; only root may change tailweave")
		} else {
			fmt.Printf("operator %s (uid %d) may change tailweave without sudo\n", s.Operator.Name, s.Operator.UID)
		}
	}
	if autoMap != nil {
		s, err := c.SetAutoMap(ctx, *autoMap)
		if err != nil {
			return err
		}
		fmt.Printf("auto-mapping %s, %d unresolved conflicts\n", onOff(s.AutoMap), unresolved(s))
	}
	return nil
}

// lookupUID returns the user ID of a user name. Without cgo, os/user only reads
// /etc/passwd, which on macOS lacks the regular users, so it falls back to id(1).
func lookupUID(name string) (int, error) {
	if u, err := user.Lookup(name); err == nil {
		return strconv.Atoi(u.Uid)
	}
	out, err := exec.Command("id", "-u", "--", name).Output()
	if err != nil {
		return 0, fmt.Errorf("unknown user %q", name)
	}
	return strconv.Atoi(strings.TrimSpace(string(out)))
}

func mapDevice(ctx context.Context, args []string) error {
	fs, socket := flags("map")
	if err := parse(fs, args, 2, 3); err != nil {
		return err
	}
	if route, err := netip.ParsePrefix(fs.Arg(1)); err == nil {
		var to netip.Prefix
		if fs.NArg() == 3 {
			if to, err = netip.ParsePrefix(fs.Arg(2)); err != nil {
				return err
			}
		}
		s, err := client.New(*socket).MapRoute(ctx, fs.Arg(0), route, to)
		if err != nil {
			return err
		}
		printRoute(s, fs.Arg(0), route)
		return nil
	}
	var ip netip.Addr
	if fs.NArg() == 3 {
		var err error
		if ip, err = netip.ParseAddr(fs.Arg(2)); err != nil {
			return err
		}
	}
	c := client.New(*socket)
	dev, err := findDevice(ctx, c, fs.Arg(0), fs.Arg(1))
	if err != nil {
		return err
	}
	s, err := c.Map(ctx, dev.Tailnet, dev.PeerID, ip)
	if err != nil {
		return err
	}
	if d := lookupDevice(s, dev.Tailnet, dev.PeerID); d != nil {
		fmt.Printf("%s (%s) is reachable at %s\n", d.Name, d.Tailnet, d.RoutedIP)
	}
	return nil
}

func unmapDevice(ctx context.Context, args []string) error {
	fs, socket := flags("unmap")
	if err := parse(fs, args, 2); err != nil {
		return err
	}
	c := client.New(*socket)
	if route, err := netip.ParsePrefix(fs.Arg(1)); err == nil {
		s, err := c.UnmapRoute(ctx, fs.Arg(0), route)
		if err != nil {
			return err
		}
		printRoute(s, fs.Arg(0), route)
		return nil
	}
	dev, err := findDevice(ctx, c, fs.Arg(0), fs.Arg(1))
	if err != nil {
		return err
	}
	s, err := c.Unmap(ctx, dev.Tailnet, dev.PeerID)
	if err != nil {
		return err
	}
	if d := lookupDevice(s, dev.Tailnet, dev.PeerID); d != nil {
		fmt.Printf("%s (%s) is reachable at %s\n", d.Name, d.Tailnet, routed(*d))
	}
	return nil
}

func serveUI(ctx context.Context, args []string) error {
	fs, socket := flags("ui")
	listen := fs.String("listen", "127.0.0.1:5250", "address of the web UI")
	if err := parse(fs, args); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: ui.Handler(*socket), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		_ = srv.Close()
	}()
	fmt.Printf("tailweave UI at http://%s (Ctrl-C to stop)\n", ln.Addr())
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
