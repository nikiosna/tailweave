# AGENTS.md

Guide for coding agents (and humans) working on tailweave. What the project is and
why: see [README.md](README.md). This file covers status, roadmap and how to work here.

The repository is meant to be published: English docs and comments, no secrets,
no personal paths. Commit locally only; pushing is done by the maintainer.

## Status and roadmap

1. **Research** – does this exist already, or is Tailscale planning it? *(open)*
2. **Linux prototype** against the lab *(in progress)*
   - [x] Lab: two headscales with identical IP ranges, peers, conflict scenarios
   - [x] Premise proven: one tailscaled per network namespace works; both instances
     get the same IP (100.64.0.3), yet 100.64.0.1 reaches peer-a1 from `tw-a` and
     peer-b1 from `tw-b` over TCP, UDP and ICMP; overlapping subnet routes
     (10.99.0.1) also resolve per namespace (`TestPlaygroundIsolation`)
   - [ ] IP mapping: primary tailnet keeps its IPs, others are mapped to their own
     range (idea: nftables NETMAP, e.g. 100.64.0.0/10 → 100.96.0.0/12 for tailnet B)
   - [ ] DNS service: route queries by domain to the right tailnet, return mapped IPs,
     replace MagicDNS (100.100.100.100)
   - [ ] Conflict detection (table in README) and resolution
   - [ ] Daemon + UI: add/log into tailnets, status, peers, conflicts
3. **macOS version** as a Network Extension *(later; Lima can also run macOS VMs)*

Keep this list current when finishing or starting a step.

## Working in this repo

- Language: Go, module `github.com/Nikiosna/tailweave`. Binaries in `cmd/`,
  internal packages in `internal/`.
- On macOS all Linux work happens in the Lima VM `tailweave` (`make vm-up`). The repo
  is mounted at the same path: edit on the host, run via `make`, which wraps
  `limactl shell` automatically. On Linux the targets run directly.
- Ad-hoc commands in the VM: `limactl shell --workdir $PWD tailweave -- <cmd>`
  (use `bash -lc '...'` when Go is needed; PATH comes from /etc/profile.d/go.sh).
- `make help` lists all targets.

### Lab

- `make lab-up SCENARIO=base|subnets|exit-nodes|dns-suffix|all`, `make lab-status`,
  `make lab-reset`. Definitions in `lab/compose.yaml`, scripts in `lab/scripts/`.
- headscale-a at http://172.30.0.10:8080, headscale-b at http://172.30.0.11:8080.
  `lab/scripts/keys.sh <a|b>` prints login server and a reusable preauth key.
- Peers start one by one, so addresses are deterministic: peer-a1/peer-b1 are
  100.64.0.1, peer-a2/peer-b2 100.64.0.2, routers .3, exit nodes .4.
- Scenarios: `subnets` (router-a and router-b both route 10.99.0.0/24 and answer on
  10.99.0.1; router-b also routes the Lima LAN 192.168.5.0/24), `exit-nodes` (exit-a,
  exit-b), `dns-suffix` (both tailnets use base domain a.lab).
- Peer test services: HTTP :80 and UDP :8 return the peer name, UDP :7 echo,
  iperf3 server. Use them to tell which tailnet answered.
- `make playground-up` runs plain tailscaled per tailnet in netns `tw-a` / `tw-b`
  (veth 10.200.<n>.0/30, control sockets `/run/tailweave-lab/<net>.sock`).
  `lab/scripts/netns-playground.sh exec <a|b> <cmd>` and `... ts <a|b> <args>`.

### Tests

- `make test`: unit tests. `make test-lab`: integration tests with build tag `lab`,
  need a running lab. Helpers in `internal/labtest`.
- CI (`.github/workflows/ci.yml`) runs both; the lab job has not run yet because
  there is no GitHub repository so far.

## Gotchas

- Lima port forwarding is disabled on purpose: guest tailscaled would collide with a
  Tailscale client on the Mac (UDP 41641).
- The lab docker network uses `gateway_mode_ipv4: nat-unprotected`; without it
  Docker >= 28 drops traffic from the namespaces to container IPs.
- Headscale CLI returns `null` instead of `[]` for empty lists; guard with `(. // [])`.
- Pass auth keys to `tailscale up` as `--auth-key=file:...` so they don't show up in `ps`.
- Do not name host interfaces `tw-*` other than the playground veths (iptables match `tw-+`).
- Pinned versions: headscale 0.29.4, tailscale 1.102.5, Go 1.27.2 (in
  `lab/compose.yaml`, `lab/lima/tailweave.yaml`, CI). Bump them together.
