# tailweave

Go project: one machine as a real node in several tailnets (see README.md, roadmap in
docs/PLAN.de.md, German). Repo is meant to be published: English docs and comments,
no secrets, no personal paths.

## Working in this repo

- Host is macOS; all Linux work happens in the Lima VM `tailweave` (`make vm-up`).
  The repo is mounted at the same path. Edit on the host, run via `make` targets,
  which wrap `limactl shell` automatically.
- Ad-hoc commands in the VM: `limactl shell --workdir $PWD tailweave -- <cmd>`
  (use `bash -lc '...'` when Go is needed, PATH comes from /etc/profile.d/go.sh).
- Lab: `make lab-up SCENARIO=base|subnets|exit-nodes|dns-suffix|all`, `make lab-status`,
  `make lab-reset`. Peers are started one by one, so peer-a1 and peer-b1 are both
  100.64.0.1, peer-a2/peer-b2 100.64.0.2.
- Peer test services: HTTP :80 returns the peer name, UDP :8 returns the peer name,
  UDP :7 echo, iperf3 server. Use them to tell which tailnet answered.
- `make playground-up` / `lab/scripts/netns-playground.sh exec <a|b> <cmd>` runs plain
  tailscaled per tailnet in netns tw-a / tw-b (control sockets /run/tailweave-lab/<net>.sock).
- Tests: `make test` (unit), `make test-lab` (build tag `lab`, needs running lab).
  Helpers in internal/labtest.

## Gotchas

- Lima port forwarding is disabled on purpose: guest tailscaled would collide with the
  host's Tailscale on UDP 41641.
- The lab docker network uses `gateway_mode_ipv4: nat-unprotected`; without it Docker
  >= 28 drops traffic from the netns to container IPs.
- Do not name host interfaces `tw-*` other than the playground veths (iptables match `tw-+`).
