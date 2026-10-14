package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Nikiosna/tailweave/internal/client"
	"github.com/Nikiosna/tailweave/internal/daemon"
)

func login(ctx context.Context, args []string) error {
	fs, socket := flags("login")
	server := fs.String("login-server", "", "control server URL (default "+daemon.DefaultLoginServer+", or the one used before)")
	hostname := fs.String("hostname", "", "this machine's name in the tailnet (default: its host name)")
	authKey := fs.String("auth-key", "", "preauth key as file:<path>, or - to read it from stdin; without it, log in via a web link")
	timeout := fs.Duration("timeout", 0, "how long to wait for the login (default 1m with a key, 10m without)")
	if err := parse(fs, args, 1); err != nil {
		return err
	}
	key, err := readAuthKey(*authKey, os.Stdin)
	if err != nil {
		return err
	}
	if *timeout == 0 {
		*timeout = 10 * time.Minute
		if key != "" {
			*timeout = time.Minute
		}
	}
	name := fs.Arg(0)
	c := client.New(*socket)
	if _, err := c.Login(ctx, daemon.LoginRequest{Name: name, LoginServer: *server, Hostname: *hostname, AuthKey: key}); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	return waitLogin(ctx, c.State, name, os.Stdout)
}

// readAuthKey resolves the -auth-key flag. Keys are not accepted on the command line,
// where other users could read them in the process list.
func readAuthKey(flag string, stdin io.Reader) (string, error) {
	var b []byte
	var err error
	switch {
	case flag == "":
		return "", nil
	case flag == "-":
		b, err = io.ReadAll(io.LimitReader(stdin, 4096))
	case strings.HasPrefix(flag, "file:"):
		b, err = os.ReadFile(strings.TrimPrefix(flag, "file:"))
	default:
		return "", errors.New("-auth-key takes file:<path> or -, not the key itself")
	}
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(b))
	if key == "" {
		return "", errors.New("the auth key is empty")
	}
	return key, nil
}

// waitLogin polls the daemon until the tailnet is logged in, printing the login URL
// when one shows up. It gives up when ctx is done or the login failed.
func waitLogin(ctx context.Context, state func(context.Context) (daemon.State, error), name string, out io.Writer) error {
	var shown string
	tick := time.NewTicker(500 * time.Millisecond)
	defer tick.Stop()
	for {
		s, err := state(ctx)
		if err != nil && ctx.Err() == nil {
			return err
		}
		for _, t := range s.Tailnets {
			if t.Name != name {
				continue
			}
			switch {
			case t.State == "Running" && t.IP.IsValid():
				fmt.Fprintf(out, "logged in to %s as %s (%s)\n", name, t.Self, t.IP)
				return nil
			case t.LoginError != "":
				return fmt.Errorf("login failed: %s", t.LoginError)
			case t.AuthURL != "" && t.AuthURL != shown:
				fmt.Fprintf(out, "To log in to %s, open:\n\n\t%s\n\n", name, t.AuthURL)
				shown = t.AuthURL
			}
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return fmt.Errorf("%s is not logged in yet; the login stays pending, see `tailweave status`", name)
			}
			return ctx.Err()
		case <-tick.C:
		}
	}
}

func logout(ctx context.Context, args []string) error {
	fs, socket := flags("logout")
	if err := parse(fs, args, 1); err != nil {
		return err
	}
	if _, err := client.New(*socket).Logout(ctx, fs.Arg(0)); err != nil {
		return err
	}
	fmt.Printf("logged out of %s; `tailweave login %s` logs in again\n", fs.Arg(0), fs.Arg(0))
	return nil
}

func remove(ctx context.Context, args []string) error {
	fs, socket := flags("remove")
	if err := parse(fs, args, 1); err != nil {
		return err
	}
	if _, err := client.New(*socket).Remove(ctx, fs.Arg(0)); err != nil {
		return err
	}
	fmt.Printf("removed %s\n", fs.Arg(0))
	return nil
}
