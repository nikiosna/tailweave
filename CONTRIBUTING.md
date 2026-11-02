# Contributing

## Development setup

Everything runs on Linux, inside a [Lima](https://lima-vm.io) VM. The Makefile
runs its targets in the VM on macOS and Linux alike, so you only need `limactl`
on the host (on Linux also QEMU with KVM). The lab and the tests change routing
tables, nftables and network namespaces and start their own tailscaled
instances; the VM keeps all of that away from your machine.

```sh
make vm-up        # create the Linux VM (Docker, Go, tailscale)
make lab-up       # two headscale servers with identical IP ranges, plus peers
make test         # unit tests
make test-lab     # integration tests against the lab
make lab-reset    # throw the lab away
```

To run directly on a Linux host instead (as CI does), set `NO_VM=1`, e.g.
`make test-lab NO_VM=1`. Only do that on a disposable machine. You then need
Docker (with compose), Go, `tailscale`/`tailscaled`, iproute2, iptables, jq and
passwordless sudo, and no Tailscale client of your own running.

The macOS version is tested in a second Lima VM running macOS (Apple Silicon host,
macOS at least as new as the guest). It joins the same lab, which keeps running in the
Linux VM:

```sh
make mac-vm-up      # create the macOS VM (downloads a ~17 GB restore image once)
make mac-test-lab   # integration tests in the macOS VM
```

## The lab

`lab/` contains two [headscale](https://github.com/juanfont/headscale) servers with
**deliberately identical IP ranges**, tailscale peers in each tailnet offering TCP, UDP
and iperf3 test services, and scenarios for the conflicts tailweave detects.

```
              DUT (tailweave / playground: one tailscaled per netns)
                 │ docker bridge 172.30.0.0/24
 ┌───────────────┼───────────────────────────────┐
 headscale-a (172.30.0.10)       headscale-b (172.30.0.11)
 peer-a1 100.64.0.1              peer-b1 100.64.0.1
 peer-a2 100.64.0.2              peer-b2 100.64.0.2
 router-a  10.99.0.0/24          router-b  10.99.0.0/24, 192.168.104.0/24
 exit-a    exit node             exit-b    exit node
                 │ wan 203.0.113.0/24 (exit nodes only)
                 inet 198.51.100.10: answers with the exit node it came through
```

```sh
make lab-up SCENARIO=all      # base | subnets | exit-nodes | dns-suffix | all
make lab-status
make run                      # the tailweave daemon
make lab-login                # second shell: log it into both tailnets
make sh                       # second shell: sudo bin/tailweave status
make ui                       # optional web UI on 127.0.0.1:5250 (make ui-tunnel)
make ui-check                 # click through the UI in headless Chromium
make mac-run                  # the daemon in the macOS VM; make mac-login, make mac-sh
```

`make help` lists all targets; [AGENTS.md](AGENTS.md) describes the lab in detail.

## Conventions

- `gofmt`, `go vet` clean
- Tests that need the lab carry the build tag `lab`
- No secrets in the repository: lab keys only live in `lab/.state/`
