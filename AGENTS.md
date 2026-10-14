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
   - [x] Merged address space (`internal/weave`): every peer keeps its real IP and is
     routed from the host into its tailnet's namespace (table 5250, masquerade per netns).
     Colliding IPs are remapped automatically (tailnet listed first keeps the IP, others
     get 198.18.0.0/15 addresses, DNAT in the netns) or, with auto-mapping off, left
     unrouted until the user maps a device by hand (`TestWeave`)
   - [x] DNS service on 100.100.100.100 (`internal/dns`), registered with
     systemd-resolved for the tailnet domains; names resolve to the routed IP
   - [x] Daemon + CLI (`tailweave run`, `internal/daemon`): peers, conflicts,
     auto-mapping toggle, manual mappings
   - [x] CLI first: API only on a Unix socket (`/run/tailweave/tailweave.sock`, root),
     `tailweave status|set|map|unmap` via `internal/client`; the web UI (`internal/ui`)
     runs only on demand with `tailweave ui`
   - [x] Daemon manages the instances itself (`internal/instance`): `tailweave login`
     creates namespace `tailweave-<name>`, veth `twh<i>`/`twn<i>` from 10.201.0.0/16 and a
     supervised tailscaled, and logs in with a preauth key (`-auth-key file:|-`);
     `logout`, `remove`; tailnets live in the state file and survive restarts
     (`TestWeave`, `TestLifecycle`)
   - [x] SSO login via web link: without a key, `tailweave login` prints the control
     server's login URL and waits; tested against Dex in the lab (`TestLoginSSO`)
   - [ ] Inbound: peers connecting to services on the host
   - [ ] Remaining conflicts: subnet routes (same merge model), exit nodes
3. **macOS version** as a Network Extension *(later; Lima can also run macOS VMs)*

Keep this list current when finishing or starting a step.

## Working in this repo

- Language: Go, module `github.com/Nikiosna/tailweave`. Binaries in `cmd/`,
  internal packages in `internal/`.
- All Linux work happens in the Lima VM `tailweave` (`make vm-up`), on macOS and Linux
  hosts alike, so the lab never touches the host's network. The repo is mounted at the
  same path: edit on the host, run via `make`, which wraps `limactl shell`
  automatically. `NO_VM=1` runs targets directly on a Linux host (used by CI).
- Ad-hoc commands in the VM: `limactl shell --workdir $PWD tailweave -- <cmd>`
  (use `bash -lc '...'` when Go is needed; PATH comes from /etc/profile.d/go.sh).
- `make help` lists all targets.

### Lab

- `make lab-up SCENARIO=base|subnets|exit-nodes|dns-suffix|all`, `make lab-status`,
  `make lab-reset`. Definitions in `lab/compose.yaml`, scripts in `lab/scripts/`.
- headscale-a at http://172.30.0.10:8080, headscale-b at http://172.30.0.11:8080.
  `lab/scripts/keys.sh <a|b>` prints login server and a reusable preauth key.
- Dex at http://172.30.0.12:5556/dex is the OIDC provider of both headscales (user
  `sso@example.com`, password `lab`). headscale only starts once Dex answers and restarts
  until then. Lab IPs are not reachable from the Mac, so complete an SSO login inside the
  VM: `lab/scripts/sso-login.sh <url>` signs in with headless Chromium and clicks
  headscale's "Confirm registration".
- Peers start one by one, so addresses are deterministic: peer-a1/peer-b1 are
  100.64.0.1, peer-a2/peer-b2 100.64.0.2, routers .3, exit nodes .4. peer-b3 exists only
  in tailnet B and starts last, so its IP does not collide with a peer in A.
- Scenarios: `subnets` (router-a and router-b both route 10.99.0.0/24 and answer on
  10.99.0.1; router-b also routes the Lima LAN 192.168.5.0/24), `exit-nodes` (exit-a,
  exit-b), `dns-suffix` (both tailnets use base domain a.lab).
- Peer test services: HTTP :80 and UDP :8 return the peer name, UDP :7 echo,
  iperf3 server. Use them to tell which tailnet answered.
- `make playground-up` runs plain tailscaled per tailnet in netns `tw-a` / `tw-b`
  (veth 10.200.<n>.0/30, control sockets `/run/tailweave-lab/<net>.sock`), without
  tailweave; only `TestPlaygroundIsolation` uses it.
  `lab/scripts/netns-playground.sh exec <a|b> <cmd>` and `... ts <a|b> <args>`.
  `down` also deletes the `dut-*` nodes from headscale.
- `make run` starts the tailweave daemon (config `lab/tailweave.json`, state in
  `/var/lib/tailweave-lab/tailweave`). `make lab-login` logs it into both tailnets as
  `dut-a`/`dut-b` with the preauth keys (`lab/scripts/login.sh`). Control it from a
  second shell (`make sh`) with `sudo bin/tailweave status` etc.
  `make ui` starts the optional web UI, `make ui-tunnel` makes it reachable on the host
  at http://127.0.0.1:5250.
- `make ui-check` clicks through the UI in headless Chromium (Playwright, installed into
  `/opt/tailweave-browser` in the VM on first use by `ensure_playwright` in
  `lab/scripts/common.sh`) and checks the effect on DNS.
  Screenshots land in `lab/.state/ui/`; look at them after UI changes.

### Tests

- `make test`: unit tests. `make test-lab`: integration tests with build tag `lab`,
  need a running lab. Helpers in `internal/labtest`. Lab test packages run one at a
  time (`-p 1`) because they share the lab and the host's network setup. Tests log in
  as `dut-<net>` and delete those nodes from headscale afterwards.
- CI (`.github/workflows/ci.yml`) runs both; the lab job has not run yet because
  there is no GitHub repository so far.

## Gotchas

- tailweave is CLI first. Everything must be doable with `tailweave <command>`; the web
  UI is optional, never started by `run`, and only talks to the daemon's API.
- Lima port forwarding is disabled on purpose: guest tailscaled would collide with a
  Tailscale client on the host (UDP 41641).
- The lab docker network uses `gateway_mode_ipv4: nat-unprotected`; without it
  Docker >= 28 drops traffic from the namespaces to container IPs.
- Headscale CLI returns `null` instead of `[]` for empty lists; guard with `(. // [])`.
- Pass auth keys to `tailscale up` as `--auth-key=file:...` so they don't show up in `ps`.
- Do not name host interfaces `tw-*` other than the playground veths (iptables match `tw-+`).
  tailweave's DNS link is `tailweave0` and its veths are `twh<i>` for that reason.
- tailweave owns routing table 5250 (ip rule pref 5250), nft table `inet tailweave`
  in each namespace and nft table `ip tailweave` on the host (masquerade for the transit
  range), plus two `twh+` ACCEPT rules in iptables `DOCKER-USER` if that chain exists
  (Docker sets FORWARD to DROP). It removes them, the namespaces and its tailscaleds on
  exit; the tailscaled state in `<stateDir>/tailnets/<name>` stays. It also sets the
  host's `net.ipv4.ip_forward=1` and leaves it on. tailscaled's own netfilter rules in
  the namespaces stay on and let the forwarded host traffic through.
- Each namespace gets `/etc/netns/<ns>/resolv.conf` with the host's real upstream servers
  (from `/run/systemd/resolve/resolv.conf`), so tailscaled can reach its control server.
- Pinned versions: headscale 0.29.4, tailscale 1.102.5, Dex v2.45.0, Go 1.27.2,
  Playwright 1.64.0 (in `lab/compose.yaml`, `lab/lima/tailweave.yaml`,
  `lab/scripts/common.sh`, CI). Bump them
  together.
