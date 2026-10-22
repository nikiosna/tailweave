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
     routed from the host into its tailnet's namespace (table 5180, masquerade per netns).
     Colliding IPs are remapped automatically (tailnet listed first keeps the IP, others
     get 198.18.0.0/15 addresses, DNAT in the netns) or, with auto-mapping off, left
     unrouted until the user maps a device by hand (`TestWeave`)
   - [x] DNS service on 100.100.100.100 (`internal/dns`), registered with
     systemd-resolved for the tailnet domains; names resolve to the routed IP
   - [x] Daemon + CLI (`tailweave run`, `internal/daemon`): peers, conflicts,
     auto-mapping toggle, manual mappings
   - [x] CLI first: API only on a Unix socket (`/run/tailweave/tailweave.sock`),
     `tailweave status|set|map|unmap` via `internal/client`; the web UI (`internal/ui`)
     runs only on demand with `tailweave ui`
   - [x] No sudo for daily use: the socket is open to all users, the daemon checks the
     caller's peer credentials; everyone may read (without pending login URLs), changes
     need root or the operator (`sudo tailweave set -operator=$USER`, kept in the state)
     (`TestAccess`; lab tests run their client as the operator)
   - [x] Daemon manages the instances itself (`internal/instance`): `tailweave login`
     creates namespace `tailweave-<name>`, veth `twh<i>`/`twn<i>` from 10.201.0.0/16 and a
     supervised tailscaled, and logs in with a preauth key (`-auth-key <key>|file:|-`);
     `logout`, `remove`; tailnets live in the state file and survive restarts
     (`TestWeave`, `TestLifecycle`)
   - [x] SSO login via web link: without a key, `tailweave login` prints the control
     server's login URL and waits; tested against Dex in the lab (`TestLoginSSO`)
   - [x] Isolation: only the host reaches the tailnets; no forwarding from the LAN, between
     namespaces, or from a tailnet out of its namespace (`TestIsolation`)
   - [x] Foreign VPNs: before touching the host, `internal/preflight` refuses to start
     when another program (Tailscale, a VPN, a fake-ip proxy) routes or holds addresses in
     CGNAT, the pool, transit or the DNS address, or uses tailweave's ip rule priority or
     table, with a hint to quit Tailscale if it is running; teardown removes only
     tailweave's own rule and routes (`TestForeignTailscale`, official client on the host)
   - [x] Inbound like with Tailscale: a peer's packet to this machine's IP in its tailnet
     ends on the host (local delivery to the transit address, never forwarded), from the
     peer's routed IP; the tailnet's ACLs decide, unrouted peers stay out; PeerAPI stays
     with Tailscale (`TestInbound` on both backends and macOS, `TestIsolation/peers`)
   - [x] Subnet routes, same merge model per prefix: the first tailnet keeps a route,
     overlapping routes of other tailnets and routes that overlap a network of the host
     (`preflight.HostNetworks`) get a pool block of the same size (netmap) or stay
     unrouted; `tailweave map <tailnet> <route> [<block>]`. 4via6 routes are routed as
     they are over IPv6 (`transit6`), never remapped, with `a-b-c-d-via-N.<domain>`
     names (`TestSubnets`, `TestVia` on both backends)
   - [x] `tailweave stop`/`start`: the daemon keeps serving its API but tears the host
     setup down (instances, DNS, backend), so another VPN can run; kept in the state
     across restarts; start runs preflight again (`TestStopStart`)
   - [ ] Remaining conflicts: exit nodes
3. **macOS version** *(in progress)*
   - [x] Backend `tsnet` (`internal/engine`, `internal/router`, `internal/hostnet`): one
     in-process tsnet engine per tailnet, each with an in-memory TUN, behind a userspace
     router that owns one host TUN (utun on macOS) and NATs 1:1 by the weave plan, with
     no state: from a tailnet it only passes packets for the engine's own IP from routed
     peers. Runs on Linux too (`"backend": "tsnet"`); the lab
     tests pass with it there
   - [x] macOS host setup: utun address, routes for 100.64.0.0/10 and the pool, DNS
     service on a lo0 alias, resolver registration via `scutil`; cross-built from the
     Linux VM
   - [x] macOS VM in the lab (`make mac-vm-up`, `make mac-test-lab`): `TestWeave`,
     `TestLifecycle`, `TestLoginSSO`, `TestForeignTailscale`, `TestInbound` pass on
     macOS 27
   - [x] First manual check on a real Mac (macOS 27): `tailweave login` into Tailscale's
     control server via login URL and into a self-hosted headscale with a preauth key
     at the same time; no IP conflicts there, peers of both answer ping by IP and HTTP
     by MagicDNS name
   - [ ] `TestIsolation` on macOS (needs a way to run commands on peers without Docker)
   - [ ] Network Extension instead of a root daemon with utun (same router, packets
     from `NEPacketTunnelFlow`)
4. **Linux on the tsnet core**: make `tsnet` the default on Linux too, then drop the netns
   backend (`internal/instance`, most of `internal/netcfg`)

Keep this list current when finishing or starting a step.

## Working in this repo

- Language: Go, module `github.com/Nikiosna/tailweave`. Binaries in `cmd/`,
  internal packages in `internal/`.
- All Linux work happens in the Lima VM `tailweave` (`make vm-up`), on macOS and Linux
  hosts alike, so the lab never touches the host's network. The repo is mounted at the
  same path: edit on the host, run via `make`, which wraps `limactl shell`
  automatically. `NO_VM=1` runs targets directly on a Linux host (used by CI).
- Backends: `netns` (default on Linux: tailscaled per network namespace, packages
  `instance` and `netcfg`) and `tsnet` (default elsewhere: `engine`, `router`,
  `hostnet`). The daemon talks to both through the `backend` interface in
  `internal/daemon/backend.go`.
- macOS work happens in a second Lima VM `tailweave-mac` with macOS (`make mac-vm-up`,
  Apple Silicon only; the first start downloads a ~17 GB restore image). The lab stays
  in the Linux VM; `lab/scripts/mac-setup.sh` routes the macOS VM to it over Lima's
  user-v2 network. After the installation the VM runs headless. There is no Go in the macOS VM: `make mac-build` cross-compiles
  `bin/darwin/tailweave` and the lab test binary in the Linux VM.
- Ad-hoc commands in the VM: `limactl shell --workdir $PWD tailweave -- <cmd>`
  (use `bash -lc '...'` when Go is needed; PATH comes from /etc/profile.d/go.sh).
- `make help` lists all targets. `make` and `make install`/`uninstall` are for users:
  they build and install natively on the host, never in the VM. The lab uses
  `make lab-build` (into `bin/`, in the VM); `make run` etc. call it.

### Lab

- `make lab-up SCENARIO=base|subnets|exit-nodes|dns-suffix|all`, `make lab-status`,
  `make lab-reset`. Definitions in `lab/compose.yaml`, scripts in `lab/scripts/`.
- headscale-a at http://172.30.0.10:8080, headscale-b at http://172.30.0.11:8080.
  `lab/scripts/keys.sh <a|b>` prints login server and a reusable preauth key.
- Dex at http://172.30.0.12:5556/dex is the OIDC provider of both headscales (user
  `sso@example.com`, password `lab`). headscale only starts once Dex answers and restarts
  until then. Lab IPs are not reachable from the Mac, so complete an SSO login inside the
  VM: `lab/scripts/sso-login.sh <url>` signs in with headless Chromium and clicks
  headscale's "Confirm registration". Tests do the same over plain HTTP
  (`labtest.SSOLogin`), so they need no browser.
- `bootstrap.sh` writes the preauth keys and headscale API keys to `lab/.state/keys.env`.
  Lab tests read them from there and use headscale's REST API, so they also run in the
  macOS VM, which has no Docker.
- Peers start one by one, so addresses are deterministic: peer-a1/peer-b1 are
  100.64.0.1, peer-a2/peer-b2 100.64.0.2, routers .3, exit nodes .4. peer-b3 exists only
  in tailnet B and starts last, so its IP does not collide with a peer in A.
- Scenarios: `subnets` (router-a and router-b both route 10.99.0.0/24 and answer on
  10.99.0.1; router-b also routes the Lima LAN 192.168.104.0/24, user-v2; the same /24
  as 4via6, site 7 on router-a and site 8 on router-b), `exit-nodes` (exit-a,
  exit-b), `dns-suffix` (both tailnets use base domain a.lab). Tests that need a
  scenario call `labtest.RequireScenario`, which also accepts `all`.
- Peer test services: HTTP :80 and UDP :8 return the peer name, UDP :7 echo,
  iperf3 server. Use them to tell which tailnet answered. TCP :9 is a probe that connects
  back on request (`lab/peer/probe.sh`: send `tcp|udp|ping <IPv4> <port>`, get the answer),
  so tests drive peers without Docker, e.g. from the macOS VM (`TestInbound`). Changes to
  `lab/peer` need `make lab-up` to rebuild the peers.
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
- `make mac-run` / `make mac-login` do the same in the macOS VM (`make mac-sh`, then
  `sudo bin/darwin/tailweave status`).
- `make ui-check` clicks through the UI in headless Chromium (Playwright, installed into
  `/opt/tailweave-browser` in the VM on first use by `ensure_playwright` in
  `lab/scripts/common.sh`) and checks the effect on DNS.
  Screenshots land in `lab/.state/ui/`; look at them after UI changes.

### Tests

- `make test`: unit tests. `make test-lab`: integration tests with build tag `lab`,
  need a running lab; the daemon tests run once per backend (`TAILWEAVE_BACKEND`).
  Helpers in `internal/labtest`. Lab test packages run one at a time (`-p 1`) because
  they share the lab and the host's network setup. Tests log in as `dut-<net>` and
  delete those nodes from headscale afterwards. `make mac-test-lab` runs the daemon tests
  in the macOS VM (`$TAILWEAVE_BIN` points them to the cross-built binary). Platform
  specific checks live in `platform_<os>_lab_test.go`.
- CI (`.github/workflows/ci.yml`) runs both, but only when started by hand
  (`workflow_dispatch`); automatic runs are off for now.

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
- tailweave owns routing table 5180 (IPv4 and IPv6; ip rule pref 5180, `iif lo`, so only locally
  generated traffic uses it; tailscaled uses table 52 and prefs 5210-5270, so never touch
  rules by priority alone, delete the exact `pref 5180 iif lo lookup 5180`), nft table `inet tailweave` in each namespace (DNAT, forward
  filter that drops new traffic from the tailnet, masquerade) and nft table `ip tailweave`
  on the host (masquerade for the transit range, forward filter that drops new traffic
  into `twh+`), plus two `twh+` ACCEPT rules in iptables `DOCKER-USER` if that chain exists
  (Docker sets FORWARD to DROP). It removes them, the namespaces and its tailscaleds on
  exit; the tailscaled state in `<stateDir>/tailnets/<name>` stays. It also sets the
  host's `net.ipv4.ip_forward=1` and leaves it on. tailscaled's own netfilter rules in
  the namespaces stay on and let the forwarded host traffic through.
- Subnet routes: tailscaled runs with `--accept-routes` (set again at every start, for
  state from older versions), tsnet with `RouteAll`. In each namespace, a rule at pref
  5200 (`lookup main suppress_prefixlength 0`, IPv4 and IPv6) keeps the veth's network
  out of table 52. The host routes each routed prefix into its namespace (table 5180),
  remapped blocks are netmapped there (`dnat ip prefix to`). Inbound only works from
  peers on netns; the stateless tsnet router also lets hosts behind a routed subnet
  route in.
- IPv6 only carries 4via6 routes, and only if the host has IPv6
  (`/proc/sys/net/ipv6/conf/all/disable_ipv6`): netns gives the veths /126s from
  `transit6` (fd74:7765:6176::/64) and turns on IPv6 forwarding only in the namespace
  (on the host it would turn off router advertisements), without an IPv6 default route
  there, so tailscaled stays on IPv4. Table 5180 and the rule at pref 5180 exist for
  IPv6 too; netns marks `fd7a:115c:a1e0:b1a::/64` unreachable there, tsnet routes it
  into the TUN, which has the first address of `transit6`. macOS asks resolvers for
  AAAA only with IPv6 connectivity (`scutil --dns` flags; neither `/etc/resolver` nor
  `__FLAGS__` in the DNS dictionary change that), so 4via6 names do not resolve in the
  macOS VM and `TestVia` skips them there.
- Inbound, netns backend: the namespace DNATs new TCP/UDP/ICMP on `ts0` for its self IP
  to the host's `twh<i>` address (except tailscaled's PeerAPI port, from the status'
  `PeerAPIURL`), forwards it to `twn<i>` only from routed peers (`ct status dnat`), and
  SNATs the real peer IP to the routed one via a map. Its postrouting chain runs at
  `srcnat - 1`, before tailscaled's, which would masquerade forwarded traffic to the
  namespace's veth address. On the host this is INPUT, the forward filter is untouched.
- Inbound, tsnet backend: with a custom `Tun`, tsnet leaves `ProcessLocalIPs` off and only
  sets `CheckLocalTransportEndpoints`, so netstack keeps packets for its own listeners,
  its own dials and the PeerAPI (in tsnet on the fake port 1) and hands everything else
  for the self IP to the router's device. The router passes TCP, UDP and ICMP echo; other
  ICMP would need its embedded header translated.
- rp_filter needs no change, even strict: the kernel validates a source with `iif lo`,
  so the reverse lookup hits table 5180, whose route leads back through the interface
  the packet came in on (`twh<i>` of the peer's tailnet, or the TUN). Verified with
  `rp_filter=1`.
- `tailweave run` first removes its own leftovers of a crashed run (`backend.Cleanup`) and
  opens the API socket; unless the state says `stopped`, it then (`Daemon.bringUp`, also
  run by `tailweave start`) runs `internal/preflight` against the host (Linux: `ip -j route/rule/addr`,
  `/proc/*/comm`; macOS: `netstat -rn`, `ifconfig`, `ps`). Default routes and /1 halves
  of full tunnels do not count; `twh*`, `tailweave0` and the interface with the transit
  host address are tailweave's own. Teardown on macOS deletes a route only if it leads
  into tailweave's utun.
- `TestForeignTailscale` runs a real tailscaled in the host namespace
  (`labtest.StartHostTailscale`: state `/var/lib/tailweave-lab/host`, socket
  `/var/run/tailweave-lab/host/tailscaled.sock`, port 41700, node `dut-host`). Linux uses
  the VM's tailscaled; `make mac-build` cross-builds tailscale/tailscaled (version from
  `go.mod`, via `go install`, so their dependencies stay out of `go.mod`) into
  `bin/darwin/`, found next to `$TAILWEAVE_BIN` or via `$TAILSCALED_BIN`/`$TAILSCALE_BIN`.
- Each namespace gets `/etc/netns/<ns>/resolv.conf` with the host's real upstream servers
  (from `/run/systemd/resolve/resolv.conf`), so tailscaled can reach its control server.
- The tsnet backend owns one TUN (`tailweave0` on Linux, the next free `utun` on macOS)
  with the first address of `transit` (10.201.0.1). Only 100.64.0.0/10 and the pool are
  routed into it (Linux: table 5180 with `iif lo`); the router drops everything not
  from 10.201.0.1, anything for an unrouted IP, and packets from a tailnet that are not
  for the engine's own IP or come from an unrouted peer. On macOS the DNS address sits on lo0 and the resolver
  settings under `State:/Network/Service/tailweave/DNS` (`scutil`); all of it goes away
  on exit. It turns off tsnet's log upload to Tailscale (`envknob.SetNoLogsNoSupport`).
- Lima's macOS guests have no passwordless sudo; `mac-setup.sh` adds
  `/etc/sudoers.d/tailweave-lab` once, with the password from `~/password` in the guest.
- Both VMs use Lima's `user-v2` network (192.168.104.0/24, the Linux VM's only
  interface); vzNAT does not work, it isolates VMs from each other. An older Linux VM
  gets it with `limactl stop tailweave && limactl edit tailweave --set
  '.networks=[{"lima":"user-v2"}]'`. `mac-setup.sh` lets that interface forward to the
  lab bridge `twlab0` (DOCKER-USER) and adds the route in the macOS VM; both are gone
  after a reboot, so the mac targets run it again.
- Lima mounts the repository at `/Volumes/My Shared Files/lima-<hash>` in macOS guests;
  `mac-setup.sh` symlinks it to its host path. The macOS installer needs a display, so
  `make mac-vm-up` creates the VM with one and then switches to `video.display: none`.
- Pinned versions: headscale 0.29.4, tailscale 1.102.5 (also the `tailscale.com` Go
  module), Dex v2.45.0, Go 1.27.2, Playwright 1.64.0 (in `lab/compose.yaml`,
  `lab/lima/tailweave.yaml`, `lab/scripts/common.sh`, `go.mod`, CI), macOS 27 for the
  macOS VM. Bump them together.
