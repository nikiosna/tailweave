// Command tailweave joins one machine to several tailnets at once.
//
// For now it attaches to tailscaled instances that already run in their own network
// namespaces (see lab/scripts/netns-playground.sh) and merges their tailnets into the
// host: routes, collision handling, DNS and a small UI. See AGENTS.md for the roadmap.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Nikiosna/tailweave/internal/daemon"
)

var version = "dev"

const usage = `usage: tailweave <command> [flags]

commands:
  run -config <file>   merge the configured tailnets into this machine (needs root)
  version              print the version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		fs := flag.NewFlagSet("run", flag.ExitOnError)
		config := fs.String("config", "/etc/tailweave/config.json", "config file")
		_ = fs.Parse(os.Args[2:])
		if err := run(*config); err != nil {
			slog.Error("tailweave", "err", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func run(config string) error {
	cfg, err := daemon.LoadConfig(config)
	if err != nil {
		return err
	}
	d, err := daemon.New(cfg)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return d.Run(ctx)
}
