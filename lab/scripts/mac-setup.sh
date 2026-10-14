#!/bin/sh
# Prepare the macOS VM and connect it to the lab: passwordless sudo (Lima does not set it
# up for macOS guests), the repository at its host path, forwarding from the user-v2
# network to the lab network in the Linux VM, and a route to the lab network in the
# macOS VM. Idempotent; runs on the host, which drives both VMs with limactl.
#
# usage: mac-setup.sh [<linux vm> [<macos vm>]]
set -eu

VM=${1:-tailweave}
MAC_VM=${2:-tailweave-mac}
LAB_NET=172.30.0.0/24
REPO=$(cd "$(dirname "$0")/../.." && pwd)

log() { printf '\033[1;34m==>\033[0m %s\n' "$*" >&2; }
mac() { limactl shell --workdir / "$MAC_VM" "$@"; }

if ! mac sudo -n true 2>/dev/null; then
	log "$MAC_VM: enabling passwordless sudo for the lab user"
	# The guest's generated password is in ~/password; sudo reads it from stdin once.
	mac sh -c 'sudo -S -p "" sh -c "echo \"$(id -un) ALL=(ALL) NOPASSWD: ALL\" > /etc/sudoers.d/tailweave-lab && chmod 440 /etc/sudoers.d/tailweave-lab" < ~/password'
fi

# Lima mounts the repository below "/Volumes/My Shared Files" in macOS guests.
if ! mac test -f "$REPO/go.mod"; then
	src=$(mac sh -c 'for d in "/Volumes/My Shared Files"/*; do [ -f "$d/go.mod" ] && echo "$d"; done' | head -1)
	[ -n "$src" ] || { echo "$MAC_VM: repository not mounted" >&2; exit 1; }
	log "$MAC_VM: linking $REPO to $src"
	mac sudo sh -c "mkdir -p '$(dirname "$REPO")' && ln -sfn '$src' '$REPO'"
fi

dev=$(limactl shell "$VM" ip -4 -o route show default | awk '{print $5; exit}')
gw=$(limactl shell "$VM" ip -4 -o addr show "$dev" | awk '{print $4; exit}' | cut -d/ -f1)
[ -n "$gw" ] || { echo "$VM: no address on $dev" >&2; exit 1; }

log "$VM ($gw): forwarding between $dev and the lab network"
limactl shell "$VM" sh -c "
	for dir in '-i $dev -o twlab0' '-i twlab0 -o $dev'; do
		sudo iptables -C DOCKER-USER \$dir -j ACCEPT 2>/dev/null || sudo iptables -I DOCKER-USER \$dir -j ACCEPT
	done"

log "$MAC_VM: route $LAB_NET via $gw"
mac sh -c "sudo route -q -n delete -net $LAB_NET >/dev/null 2>&1; sudo route -q -n add -net $LAB_NET $gw >/dev/null"
mac curl -fsS -m5 -o /dev/null http://172.30.0.10:8080/health ||
	{ echo "$MAC_VM does not reach headscale-a; is the lab up (make lab-up)?" >&2; exit 1; }
log "$MAC_VM reaches the lab"
