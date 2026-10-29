# Installing tailweave

> **Experimental.** tailweave is in an early stage. Try it on a machine where a broken
> network setup is no disaster, and expect rough edges.

tailweave is a single binary, `tailweave`. `tailweave run` is the daemon; every other
command controls it. `tailweave run` needs root. Every user on the machine may run
`tailweave status`; the commands that change something need root or the operator, a
user you allow once with `sudo tailweave set -operator=$USER`.

## Requirements

tailweave has two backends; the config field `backend` picks one.

| Platform | Backend | Needs |
| --- | --- | --- |
| Linux | `tsnet` (recommended for a first try) | `/dev/net/tun`, iproute2, systemd-resolved |
| Linux | `netns` (default on Linux) | `tailscale` and `tailscaled` 1.104.4 in `PATH`, iproute2, nftables, systemd-resolved |
| macOS (arm64, amd64) | `tsnet` (the only one) | nothing extra; uses utun, `route`, `ifconfig` and `scutil` |

- **tsnet** runs one Tailscale engine per tailnet inside the daemon, behind a single
  system TUN (`tailweave0` on Linux, a `utun` on macOS).
- **netns** runs one tailscaled per tailnet, each in its own network namespace.

Only IPv4 is supported, on both backends.

No other Tailscale client or VPN may use tailweave's address ranges: `100.64.0.0/10`,
`198.18.0.0/15` (the pool for remapped devices), `10.201.0.0/16` (transit) and
`100.100.100.100`. tailweave checks this at startup and refuses to run if they are
taken. If Tailscale is running, quit it (or run `tailscale down`) first. To go the other
way, `tailweave stop` frees them for another VPN (see below).

## Build and install

You need Go 1.27 (or a newer Go, which fetches 1.27 itself) and make.

```sh
git clone https://github.com/Nikiosna/tailweave
cd tailweave
make                  # builds ./tailweave
sudo make install     # /usr/local/bin/tailweave plus the systemd or launchd service
```

`make install` does not start anything. To try tailweave out, `sudo tailweave run` in a
terminal is enough; Ctrl-C stops it and removes its routes and DNS settings. To keep it
running, start the service, which also starts at every boot:

| | Start | Logs | Stop |
| --- | --- | --- | --- |
| Linux | `sudo systemctl enable --now tailweave` | `journalctl -u tailweave -f` | `sudo systemctl disable --now tailweave` |
| macOS | `sudo launchctl bootstrap system /Library/LaunchDaemons/com.github.nikiosna.tailweave.plist` | `tail -f /var/log/tailweave.log` | `sudo launchctl bootout system/com.github.nikiosna.tailweave` |

To switch tailweave off for a while, for example to use the official Tailscale client or
another VPN, `tailweave stop` is enough. You don't need to touch the service: the daemon
keeps running but removes all its routes, its DNS service and its interface, and stays
stopped across restarts and reboots. `tailweave start` switches it on again. If the other
VPN is still running, start refuses with the same hint as at startup. Both need root or the
operator.

To update, `git pull`, `make` and `sudo make install` again, then restart the running
service so it picks up the new binary: `sudo systemctl restart tailweave` on Linux,
`sudo launchctl kickstart -k system/com.github.nikiosna.tailweave` on macOS
(`make install` prints the right command). Your tailnets come back by themselves.

`sudo make uninstall` stops the service and removes the binary and the service file; the
state (your tailnets) stays in the state directory below. `PREFIX` (default `/usr/local`)
and `DESTDIR` work as usual.

Without make, build and copy by hand; the service files are in `contrib/`:

```sh
CGO_ENABLED=0 go build -o tailweave ./cmd/tailweave
sudo install -m 755 tailweave /usr/local/bin/tailweave
```

For another platform, set `GOOS` and `GOARCH` on the `go build`, e.g.
`GOOS=darwin GOARCH=arm64` for a Mac.

## Configuration (optional)

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
| `autoMap` | `true` | Remap colliding devices and subnet routes automatically (initial value; `tailweave set` changes it later) |
| `pool` | `198.18.0.0/15` | Addresses for remapped devices and subnet routes |
| `dns` | `100.100.100.100` | Address of tailweave's DNS service |
| `transit` | `10.201.0.0/16` | Internal addresses between the host and the tailnet instances |
| `transit6` | `fd74:7765:6176::/64` | The same for IPv6, which only carries 4via6 routes |
| `socket` | `/run/tailweave/tailweave.sock` (Linux), `/var/run/tailweave/tailweave.sock` (macOS) | Unix socket of the API |
| `stateDir` | `/var/lib/tailweave` (Linux), `/Library/Application Support/tailweave` (macOS) | State file and the Tailscale state of every tailnet |

Tailnets are not configured here; you add them with `tailweave login`, and they are
kept in the state directory.

## First steps

To drop the `sudo` in the commands below, make yourself the operator once:

```sh
sudo tailweave set -operator=$USER    # -operator= removes it again
```

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
tailweave status                    # tailnets, devices, conflicts (no sudo needed)
tailweave status home -a            # one tailnet, offline devices included
sudo tailweave map home nas         # give a device a pool address by hand
sudo tailweave unmap home nas
sudo tailweave map home 10.0.0.0/24 # the same for a subnet route: a pool block of its size
sudo tailweave unmap home 10.0.0.0/24
sudo tailweave ui                   # optional web UI on http://127.0.0.1:5250
```

Devices are reachable by name (via tailweave's DNS service) and by IP. When a device's
IP also exists in a tailnet listed earlier, it gets an address from `198.18.0.0/15`;
`tailweave status` shows which one.

Subnet routes work the same way: a route is reachable as it is unless it overlaps a
route of a tailnet listed earlier or a network of this machine (your LAN, Docker's
networks, ...). Then it gets a block of the same size from the pool, e.g.
`10.0.0.0/24` becomes `198.18.1.0/24` and `10.0.0.7` is reachable at `198.18.1.7`.
4via6 routes (`fd7a:115c:a1e0:b1a:0:<site>:<IPv4>`) are unique by design and are
routed as they are, including their names like `10-0-0-7-via-7.<tailnet domain>`.

Peers can reach services on this machine like with Tailscale (TCP, UDP and ping): each
tailnet's ACLs decide who gets in, and the host's firewall applies as usual. A remapped
device shows up with its routed address (e.g. `198.18.0.1`); a device whose IP collision
is unresolved does not get in. The tailnets never reach each other or your LAN through
tailweave.

To send this machine's internet traffic through an exit node of one of the tailnets,
pick it from a list, or name it:

```sh
sudo tailweave exit-node              # ↑/↓ and enter; "none" turns it off
sudo tailweave set -exit-node=home/nas
sudo tailweave set -exit-node=        # off again
```

All tailnets stay reachable, and so do your LAN and other networks this machine reaches
on its own. If the exit node goes offline or its tailnet is logged out, internet traffic
is blocked until it is back or you pick another one (`tailweave status` says so).

`sudo tailweave logout <tailnet>` logs out and keeps the tailnet,
`sudo tailweave remove <tailnet>` forgets it.

## Known limitations

- **Quit other VPNs first.** Tailscale and other VPN clients that use the same ranges
  cannot run next to tailweave; it refuses to start until they are gone.
- **Exit nodes carry IPv4 only.** While one is in use, IPv6 internet traffic is blocked.
  DNS queries go wherever your resolver is: through the exit node if it is on the
  internet, directly if it is on your LAN.
- **IPv6 only for 4via6 routes.** Peers' own IPv6 addresses are not routed, and 4via6
  routes that two tailnets both use stay with the tailnet listed first. On macOS, names
  like `10-0-0-7-via-7.<domain>` only resolve while the Mac has IPv6 connectivity (the
  system does not ask for IPv6 addresses otherwise); the addresses themselves always work.
- **Hosts behind subnet routes cannot connect to this machine** with the Linux default
  backend (`netns`); only peers can. With `tsnet` they can, if their subnet router
  does not masquerade them.
- **DNS settings from the admin console are ignored.** tailweave's DNS service answers
  for the peers' names; nameservers, search domains and split DNS configured in the
  tailnet are not applied.
- **Experimental.** Interfaces, config and state format may still change.
