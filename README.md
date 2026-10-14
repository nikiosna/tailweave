# tailweave

Be a real node in several tailnets at once: with TUN, UDP and MagicDNS, no proxy.
tailweave detects conflicts between the tailnets, shows them, and lets you resolve
them from the command line.

> **Status: experimental.** On Linux, tailweave runs one tailscaled per tailnet in its own
> network namespace and merges the tailnets: login with a preauth key, routing, collision
> handling, MagicDNS, a CLI and an optional web UI work.

## Why

A Tailscale client can only be in one tailnet at a time. Proxy-based workarounds only
forward TCP, so WebRTC, QUIC and programs without proxy support don't work through them.

## How it is supposed to work

- **One Tailscale instance per tailnet**, each isolated in its own network stack
  (Linux: network namespace, macOS: backend in a Network Extension).
- **One merged address space.** Every peer keeps its real tailnet IP and is routed into
  the right instance. Only when the same IP exists in several tailnets is a device
  remapped: automatically (the tailnet listed first keeps the IP, the others get an
  address from `198.18.0.0/15`) or by hand (`tailweave map`).
- **Own DNS service** replacing MagicDNS: names always resolve to the IP the device is
  actually reachable at, so collisions only matter when you type raw IPs.
- **Daemon with a CLI:** `tailweave login <name>` adds a tailnet (web link or
  `-auth-key file:<path>`), `tailweave status` shows tailnets, peers and conflicts,
  `tailweave set -auto-map=...` switches auto-mapping, `tailweave map`/`unmap` map
  devices by hand, `logout`/`remove` leave a tailnet. `tailweave ui` starts an optional
  web UI showing the same.

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
 router-a  10.99.0.0/24          router-b  10.99.0.0/24, 192.168.5.0/24
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

See [CONTRIBUTING.md](CONTRIBUTING.md) for requirements.

## License

BSD 3-Clause, see [LICENSE](LICENSE).

tailweave is an independent project and is not affiliated with or endorsed by
Tailscale Inc. "Tailscale" is a trademark of Tailscale Inc.
