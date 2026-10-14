#!/bin/sh
# Log the tailweave daemon in the macOS VM (make mac-run) into both lab tailnets as
# dut-<net>, with the preauth keys from lab/.state/keys.env. Runs on the host.
#
# usage: mac-login.sh [<linux vm> [<macos vm>]]
set -eu

VM=${1:-tailweave}
MAC_VM=${2:-tailweave-mac}
cd "$(dirname "$0")/../.."
BIN=$PWD/bin/darwin/tailweave

for net in a b; do
	case $net in a) server=http://172.30.0.10:8080 ;; b) server=http://172.30.0.11:8080 ;; esac
	key=$(sed -n "s/^KEY_$(echo $net | tr a-z A-Z)=//p" lab/.state/keys.env)
	[ -n "$key" ] || { echo "no key for tailnet $net, run make lab-up" >&2; exit 1; }
	# Old runs would linger as offline peers.
	limactl shell --workdir "$PWD" "$VM" bash -c ". lab/scripts/common.sh; forget_dut $net"
	# The key goes through stdin so it does not show up in the process list.
	printf '%s' "$key" | limactl shell "$MAC_VM" sudo "$BIN" login "$net" -login-server "$server" \
		-hostname "dut-$net" -auth-key -
done
