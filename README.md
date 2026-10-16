# tailweave

Be a real node in several tailnets at once: with TUN, UDP and MagicDNS, no proxy.
tailweave detects conflicts between the tailnets, shows them, and lets you resolve
them.

> [!WARNING]
> **Status: heavily experimental and untested.** This is just another AI-created code
> project. It has not been properly tested in real-world scenarios yet! Manage your
> expectations accordingly.
>
> Development is AI-driven and validated only by tests in VMs: a Linux VM running a lab
> with two headscale servers and their peers, and a macOS VM. See [AGENTS.md](AGENTS.md).
> The first manual check on a real Mac: logging in to Tailscale's own control server via
> its login link and to a self-hosted headscale with a preauth key, side by side, works,
> and peers of both tailnets answer ping and serve websites by their MagicDNS names.

## Install

With Go 1.27 and make, on Linux or macOS:

```sh
make && sudo make install
sudo tailweave run            # or start the service: see docs/install.md
sudo tailweave login <name>   # in a second terminal: add a tailnet
sudo tailweave set -operator=$USER   # optional: then login, map etc. work without sudo
```

Requirements, the service, configuration, first steps and known limitations:
[docs/install.md](docs/install.md).

## Why

A Tailscale client can only be in one tailnet at a time. Proxy-based workarounds only
forward TCP, so WebRTC, QUIC and programs without proxy support don't work through them.

## How it is supposed to work

![This machine runs one Tailscale instance per tailnet behind the tailweave router; both
tailnets have a peer at 100.64.0.1, the one in home appears locally as 198.18.0.1; the
tailnets do not reach each other, and the LAN does not reach them](docs/how-it-works.svg)

- **One Tailscale instance per tailnet**, each isolated in its own network stack.
  Linux: a tailscaled per network namespace. macOS (and optionally Linux): a Tailscale
  engine per tailnet inside the daemon, each with a TUN of its own in memory; a
  userspace router behind a single system TUN hands every packet to the right engine.
  Later this moves into a Network Extension.
- **One merged address space.** Every peer keeps its real tailnet IP and is routed into
  the right instance. Only when the same IP exists in several tailnets is a device
  remapped: automatically (the tailnet listed first keeps the IP, the others get an
  address from `198.18.0.0/15`) or by hand (`tailweave map`).
- **Own DNS service** replacing MagicDNS: names always resolve to the IP the device is
  actually reachable at, so collisions only matter when you type raw IPs.
- **Daemon with a CLI:** `tailweave login <name>` adds a tailnet (web link or
  `-auth-key <key>`), `tailweave status` shows tailnets, peers and conflicts,
  `tailweave set -auto-map=...` switches auto-mapping, `tailweave map`/`unmap` map
  devices by hand, `logout`/`remove` leave a tailnet. `tailweave ui` starts an optional
  web UI showing the same.

![Every peer of the tailnets work and home is a thread that tailweave weaves into one
address space on this machine; each peer keeps its IP, only nas in home, whose
100.64.0.1 is already taken by build in work, is woven in at 198.18.0.1](docs/weaving.svg)

## What reaches what

tailweave connects **this machine** to several tailnets. It does not connect the
tailnets with each other or with your LAN:

- **This machine → tailnets:** programs on the host reach every routed peer in every
  tailnet, by name or IP.
- **Tailnets → this machine:** yes, like with Tailscale. Peers reach services on the host
  through this machine's IP in their tailnet (TCP, UDP and ping); each tailnet's ACLs
  decide who gets in, and the host's own firewall still applies. A remapped peer shows
  up with the IP it is routed at here (e.g. `198.18.0.1`), so answers find their way
  back; a peer whose IP collision is unresolved does not get in.
- **Tailnet → tailnet:** no. A peer in one tailnet cannot reach a peer in another one
  through tailweave. Each instance advertises no routes, so peers only ever send it
  traffic for its own address, and that traffic ends on the host itself: tailweave
  delivers it locally and never forwards it, neither into another tailnet nor anywhere
  else. Its namespace refuses to forward anything else that arrives from the tailnet,
  and the host forwards no new connections between namespaces.
- **LAN → tailnets:** no. tailweave routes only traffic the host itself generates into
  the tailnets and drops new forwarded connections into the namespaces, so a device
  that routes `100.64.0.0/10` via your machine does not get into a tailnet. Containers
  and VMs on the host count as forwarded traffic too and don't reach the tailnets.

Programs on the host can still relay between tailnets on their own (a proxy, an SSH
tunnel, a service that one tailnet reaches and that connects into another), just as with
any two VPNs running side by side.

## Conflicts it detects

| Conflict | Resolution options |
| --- | --- |
| Same IPs in several tailnets | Remap automatically, or map a device to another IP by hand |
| Overlapping subnet routes | Ignore or map the route |
| Subnet route equals the local LAN | Ignore or map the route |
| MagicDNS (`100.100.100.100`) | tailweave's DNS service takes over |
| Several exit nodes | Allow only one |
| Same DNS names | The tailnet listed first wins |

## Development lab

`lab/` contains a reproducible test setup: two [headscale](https://github.com/juanfont/headscale)
servers with **deliberately identical IP ranges**, tailscale peers in each tailnet
offering TCP, UDP and iperf3 test services, and scenarios for the conflicts above.

```
              DUT (tailweave / playground: one tailscaled per netns)
                 │ docker bridge 172.30.0.0/24
 ┌───────────────┼───────────────────────────────┐
 headscale-a (172.30.0.10)       headscale-b (172.30.0.11)
 peer-a1 100.64.0.1              peer-b1 100.64.0.1
 peer-a2 100.64.0.2              peer-b2 100.64.0.2
 router-a  10.99.0.0/24          router-b  10.99.0.0/24, 192.168.104.0/24
 exit-a    exit node             exit-b    exit node
```

```sh
make vm-up                    # Linux VM via Lima; all targets run inside it
make lab-up SCENARIO=all      # base | subnets | exit-nodes | dns-suffix | all
make lab-status
make run                      # the tailweave daemon
make lab-login                # second shell: log it into both tailnets
make sh                       # second shell: sudo bin/tailweave status
make ui                       # optional web UI on 127.0.0.1:5250 (make ui-tunnel)
make test-lab
make ui-check                 # click through the UI in headless Chromium
make lab-reset
```

On an Apple Silicon Mac, a second VM runs macOS as the device under test, against the
same lab:

```sh
make mac-vm-up                # macOS VM via Lima, routed to the lab in the Linux VM
make mac-test-lab
make mac-run                  # the daemon in the macOS VM; make mac-login, make mac-sh
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for requirements.

## License

BSD 3-Clause, see [LICENSE](LICENSE).

tailweave is an independent project and is not affiliated with or endorsed by
Tailscale Inc. "Tailscale" is a trademark of Tailscale Inc.
