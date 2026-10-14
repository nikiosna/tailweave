#!/usr/bin/env bash
# Start tailweave, log it into both lab tailnets, start `tailweave ui` and click through
# the UI in headless Chromium (Playwright), checking that each change shows up in DNS.
# Screenshots end up in lab/.state/ui/. Needs the lab: make lab-up.
#
# usage: ui-check.sh
. "$(dirname "$0")/common.sh"

ROOT=$(cd "$LAB_DIR/.." && pwd)
OUT=$STATE_DIR/ui
TMP=$(mktemp -d)

ensure_playwright

(cd "$ROOT" && go build -o "$TMP/tailweave" ./cmd/tailweave)
jq --arg s "$TMP/state" --arg k "$TMP/tailweave.sock" '.stateDir = $s | .socket = $k' \
	"$LAB_DIR/tailweave.json" > "$TMP/config.json"
sudo "$TMP/tailweave" run -config "$TMP/config.json" > "$TMP/tailweave.log" 2>&1 &
pid=$!
sudo "$TMP/tailweave" ui -socket "$TMP/tailweave.sock" >> "$TMP/tailweave.log" 2>&1 &
uipid=$!
cleanup() {
	sudo kill "$pid" "$uipid" 2>/dev/null || true
	wait "$pid" "$uipid" 2>/dev/null || true
	sudo rm -rf "$TMP"
	for net in "${TAILNETS[@]}"; do forget_dut "$net" || true; done
}
trap cleanup EXIT
wait_for 30 "tailweave socket" sudo test -S "$TMP/tailweave.sock"
"$LAB_DIR/scripts/login.sh" "$TMP/tailweave" "$TMP/tailweave.sock"
wait_for 30 "tailweave UI" sh -c "curl -sf http://127.0.0.1:5250/api/state | jq -e '.devices | length > 0'"

rm -rf "$OUT" && mkdir -p "$OUT"
"$BROWSER_VENV/bin/python" "$LAB_DIR/scripts/ui-check.py" "$OUT" || { cat "$TMP/tailweave.log" >&2; exit 1; }
