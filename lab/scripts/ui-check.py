"""Click through tailweave's UI in headless Chromium and check the effect on DNS.

Run by ui-check.sh, which starts tailweave first. Screenshots go to the directory given
as the only argument.
"""
import json
import subprocess
import sys
import urllib.request

from playwright.sync_api import expect, sync_playwright

UI = "http://127.0.0.1:5250"
out = sys.argv[1]


def resolve(name):
    subprocess.run(["sudo", "-n", "resolvectl", "flush-caches"], check=True)
    r = subprocess.run(["getent", "hosts", name], capture_output=True, text=True)
    return r.stdout.split()[0] if r.stdout else None


def state():
    with urllib.request.urlopen(UI + "/api/state") as r:
        return json.load(r)


def device(name):
    return next(d for d in state()["devices"] if d["name"] == name)


def check(what, got, want):
    print(f"{'ok  ' if got == want else 'FAIL'} {what}: {got}")
    if got != want:
        raise SystemExit(f"{what}: got {got!r}, want {want!r}")


b1 = device("peer-b1")
name = b1["dnsName"].rstrip(".")
errors = []

with sync_playwright() as p:
    browser = p.chromium.launch()
    for scheme in ["dark", "light"]:
        page = browser.new_page(viewport={"width": 1200, "height": 900}, color_scheme=scheme)
        page.goto(UI)
        page.wait_for_selector("#devices tr")
        page.screenshot(path=f"{out}/1-start-{scheme}.png", full_page=True)
    page.on("pageerror", lambda e: errors.append(str(e)))

    check("auto-mapped name", resolve(name), b1["routedIP"])

    row = page.locator("#devices tr", has_text=name)
    row.get_by_role("button", name="Map IP").click()
    page.fill("#mapip", "198.18.0.42")
    page.get_by_role("button", name="Map", exact=True).click()
    expect(row).to_contain_text("198.18.0.42")
    expect(row).to_contain_text("mapped")
    page.screenshot(path=f"{out}/2-mapped.png", full_page=True)
    check("manually mapped name", resolve(name), "198.18.0.42")

    row = page.locator("#devices tr", has_text="peer-b2")
    row.get_by_role("button", name="Map IP").click()
    page.fill("#mapip", device("peer-a1")["realIP"])
    page.get_by_role("button", name="Map", exact=True).click()
    expect(page.locator("#msg")).to_contain_text("not free")
    page.keyboard.press("Escape")

    page.locator("#devices tr", has_text=name).get_by_role("button", name="Unmap").click()
    expect(page.locator("#devices tr", has_text=name)).to_contain_text("auto-mapped")

    page.uncheck("#automap")
    expect(page.locator("#devices tr", has_text=name)).to_contain_text("not routed")
    expect(page.locator(".conflict:not(.resolved)").first).to_be_visible()
    page.screenshot(path=f"{out}/3-automap-off.png", full_page=True)
    check("name without route", resolve(name), None)

    page.check("#automap")
    expect(page.locator("#devices tr", has_text=name)).to_contain_text("auto-mapped")

    # Subnet routes (scenario subnets): map router-b's 10.99.0.0/24 by hand, then unmap.
    if any(r["prefix"] == "10.99.0.0/24" for r in device("router-b").get("routes", [])):
        row = page.locator('#devices tr.route:has(> td:nth-child(2):text-is("b"))', has_text="10.99.0.0/24")
        row.get_by_role("button", name="Map prefix").click()
        page.fill("#mapip", "198.18.201.0/24")
        page.get_by_role("button", name="Map", exact=True).click()
        expect(row).to_contain_text("198.18.201.0/24")
        page.screenshot(path=f"{out}/5-route-mapped.png", full_page=True)
        r = subprocess.run(["curl", "-s", "-m5", "http://198.18.201.1/"], capture_output=True, text=True)
        check("router-b behind the mapped prefix", r.stdout.strip(), "router-b")
        row.get_by_role("button", name="Unmap").click()
        expect(row).to_contain_text("auto-mapped")

    # Exit nodes (scenario exit-nodes): pick exit-a, see it used, turn it off again.
    if any(d.get("exitNode") for d in state()["devices"]):
        page.select_option("#exitnode", label="exit-a (a)")
        expect(page.locator("#devices tr", has_text="exit-a")).to_contain_text("exit node in use")
        check("exit node in the state", state().get("exitNode", {}).get("name"), "exit-a")
        r = subprocess.run(["curl", "-s", "-m5", "http://198.51.100.10/"], capture_output=True, text=True)
        check("internet through exit-a", r.stdout.strip(), "203.0.113.2")
        page.screenshot(path=f"{out}/7-exit-node.png", full_page=True)
        page.select_option("#exitnode", label="None")
        expect(page.locator("#devices tr", has_text="exit-a")).not_to_contain_text("in use")
        check("exit node off", state().get("exitNode"), None)

    page.get_by_role("button", name="Stop", exact=True).click()
    expect(page.locator("#stopped")).to_be_visible()
    expect(page.locator("#devices tr")).to_have_count(0)
    page.screenshot(path=f"{out}/6-stopped.png", full_page=True)
    check("name while stopped", resolve(name), None)
    page.get_by_role("button", name="Start", exact=True).click()
    expect(page.locator("#stopped")).to_be_hidden()
    expect(page.locator("#devices tr", has_text=name)).to_contain_text("auto-mapped", timeout=30000)
    check("name after start", resolve(name), b1["routedIP"])

    mobile = browser.new_page(viewport={"width": 390, "height": 844})
    mobile.goto(UI)
    mobile.wait_for_selector("#devices tr")
    check("no horizontal page scroll on mobile",
          mobile.evaluate("document.documentElement.scrollWidth <= innerWidth"), True)
    mobile.screenshot(path=f"{out}/4-mobile.png", full_page=True)
    browser.close()

check("script errors in the page", errors, [])
print(f"screenshots in {out}")
