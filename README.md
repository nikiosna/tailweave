# tailweave

Be a real node in several tailnets at once: with TUN, UDP and MagicDNS, no proxy.
tailweave detects conflicts between the tailnets, shows them, and lets you resolve
them with a click.

> **Status: experimental.** Nothing usable yet. The lab to develop it in works.

## Why

A Tailscale client can only be in one tailnet at a time. Proxy-based workarounds only
forward TCP, so WebRTC, QUIC and programs without proxy support don't work through them.

## How it is supposed to work

- **One Tailscale instance per tailnet**, each isolated in its own network stack
  (Linux: network namespace, macOS: backend in a Network Extension).
- **One primary tailnet** keeps its real IPs. Every other tailnet is mapped to its
  own address range, so identical `100.x` addresses don't clash.
- **Own DNS service** that routes queries to the right tailnet by domain and returns
  the mapped IPs.
- **Daemon with UI:** add and log into tailnets, see status and peers, see and
  resolve conflicts.

## Conflicts it detects

| Conflict | Resolution options |
| --- | --- |
| Same IPs in several tailnets | Map a tailnet, switch primary tailnet, suggest a config change to the admin |
| Overlapping subnet routes | Ignore or map the route |
| Subnet route equals the local LAN | Ignore or map the route |
| MagicDNS (`100.100.100.100`) | tailweave's DNS service takes over |
| Several exit nodes | Allow only one |
| Same DNS suffixes | Set a priority |

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
make vm-up                    # macOS: Linux VM via Lima (skip on Linux)
make lab-up SCENARIO=all      # base | subnets | exit-nodes | dns-suffix | all
make lab-status
make playground-up            # two plain tailscaleds in netns tw-a / tw-b
make test-lab
make lab-reset
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for requirements.

## License

BSD 3-Clause, see [LICENSE](LICENSE).

tailweave is an independent project and is not affiliated with or endorsed by
Tailscale Inc. "Tailscale" is a trademark of Tailscale Inc.
