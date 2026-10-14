# Shared helpers for lab scripts. Source it, do not execute.
# The lab runs on any Linux host with Docker; on macOS run it inside the Lima VM (make vm-up).

set -euo pipefail

LAB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="$LAB_DIR/.state"
mkdir -p "$STATE_DIR"

# Tailnet name -> headscale container and control URL.
declare -A HS_CONTAINER=([a]=headscale-a [b]=headscale-b)
declare -A HS_URL=([a]=http://172.30.0.10:8080 [b]=http://172.30.0.11:8080)
TAILNETS=(a b)
LAB_USER=lab

log() { printf '\033[1;34m==>\033[0m %s\n' "$*" >&2; }
die() { printf '\033[1;31mxx\033[0m %s\n' "$*" >&2; exit 1; }

# Scenario settings are persisted so later commands (status, reset) use the same profiles.
load_scenario() {
	[ -f "$STATE_DIR/scenario.env" ] && . "$STATE_DIR/scenario.env"
	export COMPOSE_PROFILES="${COMPOSE_PROFILES:-}"
	export A_BASE_DOMAIN="${A_BASE_DOMAIN:-a.lab}" B_BASE_DOMAIN="${B_BASE_DOMAIN:-b.lab}"
	[ -f "$STATE_DIR/keys.env" ] && . "$STATE_DIR/keys.env"
	export KEY_A="${KEY_A:-}" KEY_B="${KEY_B:-}"
}

compose() { docker compose --project-directory "$LAB_DIR" -f "$LAB_DIR/compose.yaml" "$@"; }

hs() { # hs <tailnet> <headscale args...>
	local net=$1; shift
	docker exec "${HS_CONTAINER[$net]}" headscale "$@"
}

# Retry a command until it succeeds or the timeout (seconds) runs out.
wait_for() {
	local timeout=$1 what=$2; shift 2
	local end=$((SECONDS + timeout))
	until "$@" >/dev/null 2>&1; do
		[ $SECONDS -lt $end ] || die "timeout waiting for $what"
		sleep 1
	done
}
