# Contributing

tailweave is at an early, experimental stage. Issues and ideas are welcome;
for larger changes please open an issue first.

## Development setup

Everything runs on Linux. On macOS, the Makefile runs commands inside a
[Lima](https://lima-vm.io) VM, so you only need `limactl` on the host.

```sh
make vm-up        # macOS only: create the Linux VM (Docker, Go, tailscale)
make lab-up       # two headscale servers with identical IP ranges, plus peers
make test         # unit tests
make test-lab     # integration tests against the lab
make lab-reset    # throw the lab away
```

On Linux you need Docker (with compose), Go, `tailscale`/`tailscaled`,
iproute2, iptables, jq and passwordless sudo.

## Conventions

- `gofmt`, `go vet` clean
- Tests that need the lab carry the build tag `lab`
- No secrets in the repository: lab keys only live in `lab/.state/`
