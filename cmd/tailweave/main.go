// Command tailweave joins one machine to several tailnets at once.
//
// For now `tailweave run` attaches to tailscaled instances that already run in their own
// network namespaces (see lab/scripts/netns-playground.sh) and merges their tailnets into
// the host: routes, collision handling and DNS. The other commands control the running
// daemon through its Unix socket; `tailweave ui` adds an optional web UI on top. See
// AGENTS.md for the roadmap.
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
	"os/signal"
	"slices"
	"strconv"
	"syscall"
	"time"

	"github.com/Nikiosna/tailweave/internal/client"
	"github.com/Nikiosna/tailweave/internal/daemon"
	"github.com/Nikiosna/tailweave/internal/ui"
)

var version = "dev"

const usage = `usage: tailweave <command> [flags]

commands:
  run [-config <file>]               merge the configured tailnets into this machine
  status [-json]                     tailnets, devices and conflicts
  set -auto-map=<true|false>         remap colliding devices automatically or not
  map <tailnet> <device> [<ip>]      route a device at an IP (default: a free pool address)
  unmap <tailnet> <device>           remove a device's mapping
  ui [-listen <addr>]                serve a web UI for the running daemon
  version                            print the version

All commands except version need root. The commands talking to the daemon take
-socket <path> (default ` + daemon.DefaultSocket + `).
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

// flags returns a flag set for cmd with the -socket flag every daemon command has.
func flags(cmd string) (*flag.FlagSet, *string) {
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	return fs, fs.String("socket", daemon.DefaultSocket, "Unix socket of the daemon")
}

// parse parses the flags and checks that the number of remaining arguments is one of
// nargs (default: none).
func parse(fs *flag.FlagSet, args []string, nargs ...int) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			os.Exit(0)
		}
		os.Exit(2) // the flag package has printed the problem and the usage
	}
	if len(nargs) == 0 {
		nargs = []int{0}
	}
	if !slices.Contains(nargs, fs.NArg()) {
		return errUsage
	}
	return nil
}

func run(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	config := fs.String("config", "/etc/tailweave/config.json", "config file")
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
	if err := parse(fs, args); err != nil {
		return err
	}
	s, err := client.New(*socket).State(ctx)
	if err != nil {
		return err
	}
	if *asJSON {
		e := json.NewEncoder(os.Stdout)
		e.SetIndent("", "  ")
		return e.Encode(s)
	}
	printStatus(os.Stdout, s)
	return nil
}

func set(ctx context.Context, args []string) error {
	fs, socket := flags("set")
	var autoMap *bool
	fs.Func("auto-map", "remap colliding devices automatically (true|false)", func(v string) error {
		b, err := strconv.ParseBool(v)
		autoMap = &b
		return err
	})
	if err := parse(fs, args); err != nil {
		return err
	}
	if autoMap == nil {
		return errUsage
	}
	s, err := client.New(*socket).SetAutoMap(ctx, *autoMap)
	if err != nil {
		return err
	}
	fmt.Printf("auto-mapping %s, %d unresolved conflicts\n", onOff(s.AutoMap), unresolved(s))
	return nil
}

func mapDevice(ctx context.Context, args []string) error {
	fs, socket := flags("map")
	if err := parse(fs, args, 2, 3); err != nil {
		return err
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
