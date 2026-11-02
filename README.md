# tailweave

Be in your work tailnet and your home tailnet at the same time, on one machine, with
everything working: ping, UDP, WebRTC, MagicDNS names. tailweave makes this machine a
real node in several tailnets at once, with a TUN and no proxy. When the tailnets get in
each other's way, for example with the same IPs, it detects that, shows it, and resolves
it.

> [!NOTE]
> **Status: early.** tailweave works on macOS in real use: logged into Tailscale's
> control server and a self-hosted headscale at the same time. On Linux it passes the
> full lab test suite but has seen little real-world use so far. Expect rough edges.
>
> Most of the code is written with an AI coding agent and checked by integration tests
> in VMs: a Linux VM with a lab of two headscale servers and their peers, and a macOS
> VM. See [AGENTS.md](AGENTS.md).

## Why

A Tailscale client can only be in one tailnet at a time. Proxy-based workarounds only
forward TCP, so WebRTC, QUIC and programs without proxy support don't work through them.

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

## What it looks like

Two tailnets whose peers all have the same IPs (from the development lab):

```
$ tailweave status
auto-mapping on, DNS on 100.100.100.100, pool 198.18.0.0/15, no conflicts

a  Running  a.lab  dut-a  100.64.0.5  4 of 4 online
  peer-a1   100.64.0.1
  peer-a2   100.64.0.2
  router-a  100.64.0.3
            10.99.0.0/24                      route
            fd7a:115c:a1e0:b1a:0:7:a63:0/120  route
  exit-a    100.64.0.4                        exit node

b  Running  b.lab  dut-b  100.64.0.6  5 of 5 online
  peer-b1   198.18.0.1 ← 100.64.0.1           auto
  peer-b2   198.18.0.2 ← 100.64.0.2           auto
  router-b  198.18.0.3 ← 100.64.0.3           auto
            198.18.1.0/24 ← 10.99.0.0/24      route, auto
            198.18.2.0/24 ← 192.168.104.0/24  route, auto
            fd7a:115c:a1e0:b1a:0:8:a63:0/120  route
  exit-b    198.18.0.4 ← 100.64.0.4           auto, exit node
  peer-b3   100.64.0.5

$ curl peer-a1.a.lab; curl peer-b1.b.lab
peer-a1
peer-b1
```

Tailnet `a` is listed first and keeps its IPs. The peers of `b` that collide with it
are remapped into `198.18.0.0/15`, and so is its subnet route. Names always resolve to
the address a device is reachable at.

## How it works

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
  address from `198.18.0.0/15`) or by hand (`tailweave map`). Subnet routes follow the
  same model, with blocks of the same size; 4via6 routes work as they are.
- **One exit node at a time.** Pick an exit node of any tailnet; the host's internet
  traffic goes through it while all tailnets stay reachable and the LAN stays local.
  If it goes away, internet traffic is blocked rather than leaving directly.
- **Own DNS service** replacing MagicDNS: names always resolve to the IP the device is
  actually reachable at, so collisions only matter when you type raw IPs.
- **A daemon with a CLI** (below); `tailweave ui` starts an optional web UI showing the
  same.

![Every peer of the tailnets work and home is a thread that tailweave weaves into one
address space on this machine; each peer keeps its IP, only nas in home, whose
100.64.0.1 is already taken by build in work, is woven in at 198.18.0.1](docs/weaving.svg)

## Commands

| Command | What it does |
| --- | --- |
| `tailweave run` | The daemon; usually started as a service |
| `tailweave login <name>` | Add a tailnet, via web link or `-auth-key <key>` |
| `tailweave status` | Tailnets, peers and conflicts |
| `tailweave map` / `unmap` | Map a device or subnet route to another IP by hand, or undo it |
| `tailweave set -auto-map=...` | Turn automatic remapping on or off |
| `tailweave exit-node` | Pick an exit node, grouped by country and city where exit nodes say where they are (Mullvad); also `set -exit-node=<tailnet>/<device>` |
| `tailweave logout` / `remove` | Leave a tailnet, or forget it entirely |
| `tailweave stop` / `start` | Switch tailweave off to make room for another VPN, and on again |
| `tailweave ui` | Optional web UI |

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
- **Exit node:** only for the host. With an exit node picked, the host's internet
  traffic leaves through it; networks the host reaches on its own (LAN, routes of its
  own) stay direct. IPv6 internet traffic is blocked meanwhile, and DNS goes wherever the
  host's resolver is: a resolver on the internet through the exit node, one on the LAN
  directly. The LAN and the tailnets do not get to the internet through it.
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
| Overlapping subnet routes | Remap automatically to a pool block of the same size, or map the route by hand |
| Subnet route overlaps the local LAN | Remap like an overlapping route; the LAN stays where it is |
| MagicDNS (`100.100.100.100`) | tailweave's DNS service takes over |
| Several exit nodes | You pick at most one; while it is not available, internet traffic is blocked |
| Same DNS names | The tailnet listed first wins |

## Development

tailweave is tested against a reproducible lab in a Linux VM: two
[headscale](https://github.com/juanfont/headscale) servers with **deliberately identical
IP ranges**, peers in each tailnet with test services, and scenarios for every conflict
above. A second VM runs macOS against the same lab. Setup and commands are in
[CONTRIBUTING.md](CONTRIBUTING.md).

## License

BSD 3-Clause, see [LICENSE](LICENSE).

tailweave is an independent project and is not affiliated with or endorsed by
Tailscale Inc. "Tailscale" is a trademark of Tailscale Inc.
