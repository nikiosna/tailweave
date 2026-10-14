# Contributing

tailweave is at an early, experimental stage. Issues and ideas are welcome;
for larger changes please open an issue first.

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

## Conventions

- `gofmt`, `go vet` clean
- Tests that need the lab carry the build tag `lab`
- No secrets in the repository: lab keys only live in `lab/.state/`
