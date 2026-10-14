#!/bin/sh
# Connect the macOS VM to the lab: passwordless sudo in the macOS VM (Lima does not set it
# up for macOS guests), forwarding from the shared vzNAT network to the lab network in
# the Linux VM, and a route to the lab network in the macOS VM. Idempotent; runs on the
# host, which drives both VMs with limactl.
#
# usage: mac-setup.sh [<linux vm> [<macos vm>]]
set -eu

VM=${1:-tailweave}
MAC_VM=${2:-tailweave-mac}
LAB_NET=172.30.0.0/24

log() { printf '\033[1;34m==>\033[0m %s\n' "$*" >&2; }

if ! limactl shell "$MAC_VM" sudo -n true 2>/dev/null; then
	log "$MAC_VM: enabling passwordless sudo for the lab user"
	# The guest's generated password is in ~/password; sudo reads it from stdin once.
	limactl shell "$MAC_VM" sh -c 'cat ~/password | sudo -S -p "" sh -c "echo \"$(id -un) ALL=(ALL) NOPASSWD: ALL\" > /etc/sudoers.d/tailweave-lab && chmod 440 /etc/sudoers.d/tailweave-lab"'
fi

gw=$(limactl shell "$VM" ip -4 -o addr show lima0 | awk '{print $4}' | cut -d/ -f1)
[ -n "$gw" ] || { echo "$VM has no vzNAT address on lima0; recreate it with make vm-destroy vm-up" >&2; exit 1; }

log "$VM ($gw): forwarding between vzNAT and the lab network"
limactl shell "$VM" sh -c '
	for dir in "-i lima0 -o twlab0" "-i twlab0 -o lima0"; do
		sudo iptables -C DOCKER-USER $dir -j ACCEPT 2>/dev/null || sudo iptables -I DOCKER-USER $dir -j ACCEPT
	done'

log "$MAC_VM: route $LAB_NET via $gw"
limactl shell "$MAC_VM" sh -c "sudo route -q -n delete -net $LAB_NET >/dev/null 2>&1; sudo route -q -n add -net $LAB_NET $gw"
limactl shell "$MAC_VM" curl -fsS -m5 -o /dev/null http://172.30.0.10:8080/health &&
	log "$MAC_VM reaches headscale-a"
