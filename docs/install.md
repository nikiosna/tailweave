# Installing tailweave

> **Experimental.** tailweave is in an early stage. Try it on a machine where a broken
> network setup is no disaster, and expect rough edges.

tailweave is a single binary, `tailweave`. `tailweave run` is the daemon; every other
command controls it. All commands except `tailweave version` need root.

## Requirements

tailweave has two backends; the config field `backend` picks one.

| Platform | Backend | Needs |
| --- | --- | --- |
| Linux | `tsnet` (recommended for a first try) | `/dev/net/tun`, iproute2, systemd-resolved |
| Linux | `netns` (default on Linux) | `tailscale` and `tailscaled` 1.102.5 in `PATH`, iproute2, nftables, systemd-resolved |
| macOS (arm64, amd64) | `tsnet` (the only one) | nothing extra; uses utun, `route`, `ifconfig` and `scutil` |

- **tsnet** runs one Tailscale engine per tailnet inside the daemon, behind a single
  system TUN (`tailweave0` on Linux, a `utun` on macOS).
- **netns** runs one tailscaled per tailnet, each in its own network namespace.

Only IPv4 is supported, on both backends.

No other Tailscale client or VPN may use tailweave's address ranges: `100.64.0.0/10`,
`198.18.0.0/15` (the pool for remapped devices), `10.201.0.0/16` (transit) and
`100.100.100.100`. tailweave checks this at startup and refuses to run if they are
taken. If Tailscale is running, quit it (or run `tailscale down`) first.

## Build from source

You need Go 1.27.

```sh
git clone https://github.com/Nikiosna/tailweave
cd tailweave
CGO_ENABLED=0 go build -ldflags "-X main.version=$(git describe --always --dirty)" \
	-o tailweave ./cmd/tailweave
```

`-ldflags "-X main.version=..."` sets what `tailweave version` prints; without it, it
prints `dev`. For another platform, set `GOOS` and `GOARCH`, e.g. for a Mac:

```sh
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build \
	-ldflags "-X main.version=$(git describe --always --dirty)" -o tailweave ./cmd/tailweave
```

Inside this repository, `make build` does the same and puts the binary in `bin/`.

## Install

```sh
sudo install -m 755 tailweave /usr/local/bin/tailweave
```

### Configuration (optional)

tailweave runs without a config file. To change a default, create
`/etc/tailweave/config.json` with only the fields you need:

```json
{
  "backend": "tsnet",
  "autoMap": true
}
```

| Field | Default | Meaning |
| --- | --- | --- |
| `backend` | `netns` on Linux, `tsnet` elsewhere | `netns` or `tsnet` |
| `autoMap` | `true` | Remap colliding devices automatically (initial value; `tailweave set` changes it later) |
| `pool` | `198.18.0.0/15` | Addresses for remapped devices |
| `dns` | `100.100.100.100` | Address of tailweave's DNS service |
| `transit` | `10.201.0.0/16` | Internal addresses between the host and the tailnet instances |
| `socket` | `/run/tailweave/tailweave.sock` (Linux), `/var/run/tailweave/tailweave.sock` (macOS) | Unix socket of the API |
| `stateDir` | `/var/lib/tailweave` (Linux), `/Library/Application Support/tailweave` (macOS) | State file and the Tailscale state of every tailnet |

Tailnets are not configured here; you add them with `tailweave login`, and they are
kept in the state directory.

## Run as a service

To try it out, `sudo tailweave run` in a terminal is enough; Ctrl-C stops it and removes
its routes and DNS settings. For permanent use, install it as a service.

### Linux (systemd)

```sh
sudo install -m 644 contrib/systemd/tailweave.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now tailweave
journalctl -u tailweave -f          # logs
```

### macOS (launchd)

```sh
sudo install -m 644 -o root -g wheel contrib/launchd/com.github.nikiosna.tailweave.plist \
	/Library/LaunchDaemons/
sudo launchctl bootstrap system /Library/LaunchDaemons/com.github.nikiosna.tailweave.plist
tail -f /var/log/tailweave.log      # logs
```

Stop it with `sudo launchctl bootout system/com.github.nikiosna.tailweave`.

## First steps

Add a tailnet on Tailscale's control server. Without a key, `login` prints a link; open
it in a browser and sign in:

```sh
sudo tailweave login work
```

Add a second one, e.g. on a self-hosted [headscale](https://github.com/juanfont/headscale):

```sh
sudo tailweave login home -login-server https://headscale.example.com
```

Instead of the web link, `-auth-key file:<path>` (or `-auth-key -` for stdin) logs in
with a preauth key. Then:

```sh
sudo tailweave status               # tailnets, devices, conflicts
sudo tailweave map home nas         # give a device a pool address by hand
sudo tailweave unmap home nas
sudo tailweave ui                   # optional web UI on http://127.0.0.1:5250
```

Devices are reachable by name (via tailweave's DNS service) and by IP. When a device's
IP also exists in a tailnet listed earlier, it gets an address from `198.18.0.0/15`;
`tailweave status` shows which one.

Peers can reach services on this machine like with Tailscale (TCP, UDP and ping): each
tailnet's ACLs decide who gets in, and the host's firewall applies as usual. A remapped
device shows up with its routed address (e.g. `198.18.0.1`); a device whose IP collision
is unresolved does not get in. The tailnets never reach each other or your LAN through
tailweave.

`sudo tailweave logout <tailnet>` logs out and keeps the tailnet,
`sudo tailweave remove <tailnet>` forgets it.

## Known limitations

- **Quit other VPNs first.** Tailscale and other VPN clients that use the same ranges
  cannot run next to tailweave; it refuses to start until they are gone.
- **No subnet routes and no exit nodes** yet. Only the peers themselves are reachable.
- **IPv4 only.** Peers' IPv6 addresses are not routed.
- **DNS settings from the admin console are ignored.** tailweave's DNS service answers
  for the peers' names; nameservers, search domains and split DNS configured in the
  tailnet are not applied.
- **Experimental.** Interfaces, config and state format may still change.
