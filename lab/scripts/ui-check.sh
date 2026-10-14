#!/usr/bin/env bash
# Start tailweave against the playground and click through its UI in headless Chromium
# (Playwright), checking that each change shows up in DNS. Screenshots end up in
# lab/.state/ui/. Needs the lab and the playground: make lab-up && make playground-up.
#
# usage: ui-check.sh
. "$(dirname "$0")/common.sh"

PLAYWRIGHT_VERSION=1.64.0
VENV=/opt/tailweave-browser
ROOT=$(cd "$LAB_DIR/.." && pwd)
OUT=$STATE_DIR/ui
TMP=$(mktemp -d)

if ! "$VENV/bin/python" -c "import playwright" 2>/dev/null; then
	log "installing Playwright $PLAYWRIGHT_VERSION and Chromium into $VENV"
	command -v apt-get >/dev/null && ! python3 -c 'import ensurepip' 2>/dev/null &&
		sudo DEBIAN_FRONTEND=noninteractive apt-get install -y -qq python3-venv >/dev/null
	sudo python3 -m venv "$VENV"
	sudo chown -R "$(id -u):$(id -g)" "$VENV"
	"$VENV/bin/pip" install -q "playwright==$PLAYWRIGHT_VERSION"
	sudo "$VENV/bin/playwright" install-deps chromium >/dev/null
	"$VENV/bin/playwright" install chromium
fi

(cd "$ROOT" && go build -o "$TMP/tailweave" ./cmd/tailweave)
jq --arg s "$TMP/state.json" '.state = $s' "$LAB_DIR/tailweave.json" > "$TMP/config.json"
sudo "$TMP/tailweave" run -config "$TMP/config.json" > "$TMP/tailweave.log" 2>&1 &
pid=$!
cleanup() {
	sudo kill "$pid" 2>/dev/null || true
	wait "$pid" 2>/dev/null || true
	rm -rf "$TMP"
}
trap cleanup EXIT
wait_for 30 "tailweave UI" sh -c "curl -sf http://127.0.0.1:5250/api/state | jq -e '.devices | length > 0'"

rm -rf "$OUT" && mkdir -p "$OUT"
"$VENV/bin/python" "$LAB_DIR/scripts/ui-check.py" "$OUT" || { cat "$TMP/tailweave.log" >&2; exit 1; }
