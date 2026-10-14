#!/usr/bin/env bash
# Log a running tailweave into both lab tailnets as dut-<net>, with their preauth keys.
# Tailnets that are already logged in are left alone.
#
# usage: login.sh [<tailweave binary> [<socket>]]
. "$(dirname "$0")/common.sh"

BIN=${1:-$LAB_DIR/../bin/tailweave}
SOCK=${2:-/run/tailweave/tailweave.sock}

for net in "${TAILNETS[@]}"; do
	if sudo "$BIN" status -json -socket "$SOCK" |
		jq -e --arg n "$net" '(.tailnets // [])[] | select(.name == $n and .state == "Running")' >/dev/null; then
		log "$net: already logged in"
		continue
	fi
	forget_dut "$net"
	read -r url key < <("$(dirname "$0")/keys.sh" "$net")
	# The key goes through stdin so it does not show up in the process list.
	printf '%s' "$key" | sudo "$BIN" login "$net" -socket "$SOCK" -login-server "$url" \
		-hostname "dut-$net" -auth-key -
done
