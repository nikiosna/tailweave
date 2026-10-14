#!/usr/bin/env bash
# Complete an SSO login as the lab's Dex user in headless Chromium: open the login URL
# that `tailweave login` printed, sign in at Dex and wait for headscale to confirm.
#
# usage: sso-login.sh <login-url>
. "$(dirname "$0")/common.sh"

url=${1:?usage: sso-login.sh <login-url>}
ensure_playwright
exec "$BROWSER_VENV/bin/python" "$LAB_DIR/scripts/sso-login.py" "$url" "$SSO_USER" "$SSO_PASSWORD"
