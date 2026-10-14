"""Sign in at Dex for a headscale login URL, confirm the registration and wait until
headscale has registered the machine. Run by sso-login.sh with the URL, user and
password as arguments.
"""
import sys

from playwright.sync_api import sync_playwright

url, user, password = sys.argv[1:4]

with sync_playwright() as p:
    page = p.chromium.launch().new_page()
    page.set_default_timeout(20000)
    page.goto(url)
    # headscale redirects to Dex, which shows its password form right away because the
    # password database is its only connector.
    page.fill("#login", user)
    page.fill("#password", password)
    page.click("#submit-login")
    # Back at headscale, which asks to confirm the registration of this machine.
    page.wait_for_url("**/register/confirm/**")
    with page.expect_response(lambda r: "/register/confirm/" in r.url and r.request.method == "POST") as confirm:
        page.click("text=Confirm registration")
    resp = confirm.value
    page.wait_for_load_state()
    text = page.inner_text("body").strip()
    print(f"headscale answered {resp.status}: {' '.join(text.split())[:120]}")
    if resp.status != 200:
        raise SystemExit(f"registration failed with {resp.status}:\n{text}")
