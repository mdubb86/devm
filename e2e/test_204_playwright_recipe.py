"""204: Playwright recipe end-to-end — Chromium + Firefox + WebKit all
trust the devm CA on an in-project HTTPS hairpin.

Proves recipes/tool/playwright.md works on a real Tart VM across all
three Playwright browsers, pinning the recipe's promises in one go:

  A. Core packages installed. p11-kit-modules and nodejs both `ii`;
     /usr/lib/aarch64-linux-gnu/pkcs11/p11-kit-trust.so exists. Rules
     out an apt or base-image regression.

  B. All three browsers downloaded. chromium-*, firefox-*/firefox/,
     webkit-*/pw_run.sh under ~/.cache/ms-playwright.

  C. Firefox NSS db seeded. certutil -L on the pre-built Firefox
     profile at /home/devm/.pw-profiles/firefox lists the devm CA with
     trust flag "C,,". Linux-Firefox has no OS-trust-store integration
     and Playwright patches enterprise-policy enforcement out of its
     test build, so a per-profile cert9.db is the only portable
     trust mechanism; launchPersistentContext reuses that profile at
     launch.

  D. Hairpin HTTP service is up. curl http://127.0.0.1:8080/data.json
     from inside the guest returns the fixture JSON — proves the app
     service came up and supervisor is keeping it alive.

  E. THE trust assertion. Playwright spec visits
     https://app.<proj>.e2e.test/ under three browsers, each via
     launchPersistentContext from its pre-built profile dir. Iron-proxy
     MITMs with a devm-CA-signed cert and hairpins back to port 8080.
     The page runs JS, fetches /data.json over HTTPS (a second trust
     check), and writes "yes" into #r. Playwright asserts
     locator('#r') text equals "yes" per browser. Output must contain
     "3 passed".

Trust chain per browser (what the E assertion actually proves):
  - Chromium → NSS at /home/devm/.pki/nssdb (seeded by install.sh).
  - Firefox  → per-profile cert9.db at /home/devm/.pw-profiles/firefox
               (seeded by `certutil -A` in install()).
  - WebKit   → libsoup → glib-networking → GnuTLS → p11-kit-trust
               → /etc/ssl/certs/ca-certificates.crt.

Live-network test: needs cdn.playwright.dev, Microsoft's mirror,
Chrome-for-Testing's bucket (browser downloads) and registry.npmjs.org
(@playwright/test). The assertions themselves need no external
network — the HTTPS endpoint is served from the guest's own port 8080
and MITM'd by iron-proxy.

`recipe` marker — slow (cold-start + ~1.5 GB of browser downloads).
Run via `just e2e-recipe test_204_playwright_recipe`.
"""
from __future__ import annotations

import base64
import subprocess
import textwrap

import pytest

pytestmark = pytest.mark.recipe


# --- Fixture files written into the guest by install:. Kept here as
# --- plain strings; install: decodes each from base64 to avoid YAML/
# --- shell quoting hazards. The files live under /home/devm/app so
# --- the devm user owns them without sudo.

SERVE_APP_PY = """\
import http.server, socketserver, json
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/data.json":
            body = json.dumps({"browser_trusts_ca": "yes"}).encode()
            ctype = "application/json"
        else:
            body = (b"<!doctype html><title>app</title>"
                    b"<div id='r'>loading</div>"
                    b"<script>fetch('/data.json').then(r=>r.json())"
                    b".then(j=>{document.getElementById('r').textContent=j.browser_trusts_ca})</script>")
            ctype = "text/html; charset=utf-8"
        self.send_response(200)
        self.send_header("Content-Type", ctype)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)
    def log_message(self, *a, **k): pass
socketserver.TCPServer(("0.0.0.0", 8080), H).serve_forever()
"""

PLAYWRIGHT_CONFIG_TS = """\
import { defineConfig } from '@playwright/test';
export default defineConfig({
  timeout: 60_000,
});
"""

# The spec runs one test per browser sequentially, each launching a
# persistent context from a pre-seeded profile dir. install() seeds
# Firefox's profile cert9.db with the devm CA via certutil — Firefox
# on Linux has no OS-trust-store integration (no stock
# `security.enterprise_roots.enabled` on Linux, and Playwright's
# Firefox ignores enterprise policy files), so per-profile NSS seeding
# is the only portable mechanism. Chromium and WebKit don't need a
# seeded profile — they read NSS at ~/.pki/nssdb and GnuTLS's system
# CA bundle respectively, both already carrying the devm CA.
APP_SPEC_TS = """\
import { test, expect, chromium, firefox, webkit } from '@playwright/test';

const host = process.env.TEST_HOST!;
const LAUNCHERS = { chromium, firefox, webkit } as const;

for (const name of ['chromium', 'firefox', 'webkit'] as const) {
  test(`${name}: loads over HTTPS and renders JS result`, async () => {
    const ctx = await LAUNCHERS[name].launchPersistentContext(
      `/home/devm/.pw-profiles/${name}`,
    );
    try {
      const page = await ctx.newPage();
      await page.goto(`https://${host}/`);
      await expect(page.locator('#r')).toHaveText('yes', { timeout: 15_000 });
    } finally {
      await ctx.close();
    }
  });
}
"""


def _b64(s: str) -> str:
    return base64.b64encode(s.encode()).decode()


@pytest.mark.timeout(1800)
@pytest.mark.slow
def test_playwright_recipe(devm, workspace, sandbox_name):
    proj = workspace.vm_name
    host = f"app.{proj}.e2e.test"

    workspace.devm_yaml_path.write_text(textwrap.dedent(f"""\
        project:
          name: {proj}
        packages:
          # Recipe's existing chromium baseline (brings nss/glib/x11 transitively).
          - chromium
          # Explicit: Firefox NSS bridge (loaded by /etc/firefox/policies/policies.json)
          # and WebKit's GnuTLS backend both read the p11-kit-trust module.
          - p11-kit-modules
          # Node for @playwright/test.
          - nodejs
          - npm
          # Firefox runtime libs — verbatim from Playwright's
          # packages/playwright-core/src/server/registry/nativeDeps.ts
          # (debian13 → firefox) at the v1.65.1 tag.
          - libasound2
          - libavcodec61
          - libcairo-gobject2
          - libdbus-glib-1-2
          - libfontconfig1
          - libfreetype6
          - libgdk-pixbuf-2.0-0
          - libgtk-3-0t64
          - libharfbuzz0b
          - libpangocairo-1.0-0
          - libx11-xcb1
          - libxcb-shm0
          - libxcursor1
          - libxi6
          - libxrender1
          - libxtst6
          # WebKit runtime libs — same source, debian13 → webkit.
          - libsoup-3.0-0
          - gstreamer1.0-libav
          - gstreamer1.0-plugins-bad
          - gstreamer1.0-plugins-base
          - gstreamer1.0-plugins-good
          - libegl1
          - libenchant-2-2
          - libepoxy0
          - libevdev2
          - libgles2
          - libglx0
          - libgstreamer-gl1.0-0
          - libgstreamer-plugins-base1.0-0
          - libgstreamer1.0-0
          - libgtk-4-1
          - libgudev-1.0-0
          - libharfbuzz-icu0
          - libhyphen0
          - libicu76
          - libjpeg62-turbo
          - liblcms2-2
          - libmanette-0.2-0
          - libnotify4
          - libopengl0
          - libopenjp2-7
          - libopus0
          - libpng16-16t64
          - libproxy1v5
          - libsecret-1-0
          - libwayland-client0
          - libwayland-egl1
          - libwayland-server0
          - libwebp7
          - libwebpdemux2
          - libwoff1
          - libxml2
          - libxslt1.1
          - libatomic1
          - libevent-2.1-7t64
          - libavif16
        network:
          allow:
            - cdn.playwright.dev
            - playwright.download.prss.microsoft.com
            - storage.googleapis.com/chrome-for-testing-public/*
            - registry.npmjs.org
        volumes:
          pwcache: /home/devm/.cache/ms-playwright
        services:
          app:
            port: 8080
            hostname: {host}
            exec: ["python3", "/home/devm/app/serve_app.py"]
            restart: always
    """))

    # devm.sh — install:/startup: as devm.yaml top-level fields were
    # removed; provisioning now comes from devm.sh functions.
    #
    # install() decodes the fixture files, bootstraps npm, downloads all
    # three browsers, then builds one pre-seeded persistent profile per
    # browser under /home/devm/.pw-profiles/. Firefox is the load-bearing
    # case: Linux-Firefox has no OS-trust-store integration and
    # Playwright patches enterprise policies out of its build, so the
    # ONLY trust mechanism is per-profile NSS seeding via `certutil`.
    # Chromium and WebKit don't strictly need pre-seeded profiles (they
    # read NSS and system CA respectively), but a persistent context
    # per browser keeps the spec uniform.
    workspace.write_devm_sh(textwrap.dedent(f"""\
        #!/usr/bin/env bash
        set -eo pipefail

        install() {{
          mkdir -p /home/devm/app/tests
          echo {_b64(SERVE_APP_PY)} | base64 -d > /home/devm/app/serve_app.py
          echo {_b64(PLAYWRIGHT_CONFIG_TS)} | base64 -d > /home/devm/app/playwright.config.ts
          echo {_b64(APP_SPEC_TS)} | base64 -d > /home/devm/app/tests/app.spec.ts
          cd /home/devm/app
          npm init -y
          npm install --save-dev @playwright/test@1.65.1
          npx --yes playwright install chromium firefox webkit

          # Build per-browser persistent profile dirs. certutil -N
          # initializes an empty cert9.db (sql format, empty password);
          # certutil -A imports devm's CA with trust flag "C,," (trust
          # issuer for TLS server certs). The dir must survive between
          # install() and spec runs; /home/devm/.pw-profiles persists
          # until teardown.
          for b in chromium firefox webkit; do
            mkdir -p /home/devm/.pw-profiles/$b
            certutil -N -d sql:/home/devm/.pw-profiles/$b --empty-password
            certutil -A \\
              -n devm \\
              -t "C,," \\
              -d sql:/home/devm/.pw-profiles/$b \\
              -i /opt/devm/ca/devm.crt
          done
        }}
    """))

    try:
        # Cold-start. Budget covers apt (large list) + npm install +
        # ~1.5 GB of browser downloads from three CDNs. The tail is the
        # browser downloads; a warm pwcache snapshot (set up by a prior
        # successful run) short-circuits them.
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=1500,
        )
        assert r.returncode == 0, (
            f"cold-start failed:\n"
            f"stdout:\n{r.stdout.decode(errors='replace')}\n"
            f"stderr:\n{r.stderr.decode(errors='replace')}"
        )

        # A. Core packages installed + p11-kit-trust.so present at the
        # hardcoded aarch64 path the recipe's trust bridge depends on.
        r = subprocess.run(
            [devm.path, "shell", "--", "bash", "-c",
             "dpkg -l p11-kit-modules nodejs | awk '$1==\"ii\"{print $2}' | sort && "
             "ls /usr/lib/aarch64-linux-gnu/pkcs11/p11-kit-trust.so"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, (
            f"core-packages check failed:\n{r.stderr.decode()}"
        )
        out = r.stdout.decode()
        for pkg in ("p11-kit-modules", "nodejs"):
            assert pkg in out, f"expected {pkg} installed; got:\n{out}"
        assert "p11-kit-trust.so" in out, (
            f"expected /usr/lib/aarch64-linux-gnu/pkcs11/p11-kit-trust.so "
            f"present; got:\n{out}"
        )

        # B. All three browsers downloaded. Globs resolve against
        # the Playwright version revision dir.
        r = subprocess.run(
            [devm.path, "shell", "--", "bash", "-c",
             "ls -d /home/devm/.cache/ms-playwright/chromium-*/ "
             "/home/devm/.cache/ms-playwright/firefox-*/firefox/ "
             "/home/devm/.cache/ms-playwright/webkit-*/pw_run.sh"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, (
            f"browser-presence check failed — one of chromium/firefox/webkit "
            f"didn't download:\nstdout:\n{r.stdout.decode()}\n"
            f"stderr:\n{r.stderr.decode()}"
        )
        for key in ("chromium-", "firefox-", "webkit-"):
            assert key in r.stdout.decode(), (
                f"missing {key} in browser cache listing:\n{r.stdout.decode()}"
            )

        # C. Per-browser profile dirs exist and the Firefox profile
        # carries the devm CA. Firefox on Linux has no OS-trust-store
        # integration and Playwright patches enterprise policies out
        # of its build, so this seeded NSS db is the only trust
        # mechanism. If certutil didn't run or the import failed, the
        # E assertion catches it — this one tells you where.
        r = subprocess.run(
            [devm.path, "shell", "--", "bash", "-c",
             "certutil -L -d sql:/home/devm/.pw-profiles/firefox"],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert r.returncode == 0, (
            f"Firefox profile NSS db unreadable:\n{r.stderr.decode()}"
        )
        assert b"devm" in r.stdout, (
            f"devm CA not imported into Firefox profile NSS db; got:\n"
            f"{r.stdout.decode()}"
        )

        # D. App service up on 8080. Loopback curl bypasses iron-proxy
        # entirely — proves the Python server itself is running under
        # supervisor before Playwright's TLS path exercises it.
        r = subprocess.run(
            [devm.path, "shell", "--", "bash", "-c",
             "curl -sf http://127.0.0.1:8080/data.json"],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert r.returncode == 0, (
            f"app service not up on 8080:\n{r.stderr.decode()}"
        )
        assert b"browser_trusts_ca" in r.stdout and b"yes" in r.stdout, (
            f"app service returned unexpected payload: {r.stdout!r}"
        )

        # E. THE trust assertion. Playwright spec against the HTTPS
        # hairpin, under three browser projects. Each project:
        #   - opens TCP to InterceptedTestIP:443,
        #   - iron-proxy terminates TLS with a devm-CA-signed cert,
        #   - browser accepts it (proves trust via its own store),
        #   - page JS runs and fetches /data.json (second TLS handshake),
        #   - "yes" appears in #r.
        # If any one browser fails its trust check, its project fails
        # and "3 passed" is not in the output.
        r = subprocess.run(
            [devm.path, "shell", "--", "bash", "-c",
             f"cd /home/devm/app && TEST_HOST={host} npx playwright test"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        combined = (
            r.stdout.decode(errors="replace")
            + r.stderr.decode(errors="replace")
        )
        assert r.returncode == 0, (
            f"Playwright test failed (exit {r.returncode}):\n{combined}"
        )
        assert "3 passed" in combined, (
            f"expected '3 passed' in Playwright output; got:\n{combined}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
