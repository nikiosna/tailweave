# Shared helpers for lab scripts. Source it, do not execute.
# The lab runs on any Linux host with Docker; normally that is the Lima VM (make vm-up).

set -euo pipefail

LAB_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE_DIR="$LAB_DIR/.state"
mkdir -p "$STATE_DIR"

# Tailnet name -> headscale container and control URL.
declare -A HS_CONTAINER=([a]=headscale-a [b]=headscale-b)
declare -A HS_URL=([a]=http://172.30.0.10:8080 [b]=http://172.30.0.11:8080)
TAILNETS=(a b)
LAB_USER=lab

# The Dex user for SSO logins (lab/dex/config.yaml).
SSO_USER=sso@example.com
SSO_PASSWORD=lab

# Headless Chromium for UI checks and SSO logins, installed on first use.
PLAYWRIGHT_VERSION=1.64.0
BROWSER_VENV=/opt/tailweave-browser

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

# Remove the machine's own lab nodes (dut-<net>) from headscale, so old runs do not
# linger as offline peers.
forget_dut() {
	local net=$1 id
	for id in $(hs "$net" nodes list -o json | jq -r --arg p "dut-$net" \
		'(. // [])[] | select(.given_name | startswith($p)) | .id'); do
		hs "$net" nodes delete --identifier "$id" --force >/dev/null
	done
}

ensure_playwright() {
	"$BROWSER_VENV/bin/python" -c "import playwright" 2>/dev/null && return
	log "installing Playwright $PLAYWRIGHT_VERSION and Chromium into $BROWSER_VENV"
	command -v apt-get >/dev/null && ! python3 -c 'import ensurepip' 2>/dev/null &&
		sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq python3-venv >/dev/null
	sudo python3 -m venv "$BROWSER_VENV"
	sudo chown -R "$(id -u):$(id -g)" "$BROWSER_VENV"
	"$BROWSER_VENV/bin/pip" install -q "playwright==$PLAYWRIGHT_VERSION"
	sudo "$BROWSER_VENV/bin/playwright" install-deps chromium >/dev/null
	"$BROWSER_VENV/bin/playwright" install chromium
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
