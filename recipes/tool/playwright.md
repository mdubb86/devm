---
name: tool/tool/playwright
category: tool
display_name: Playwright
description: "Browser e2e inside the VM with Playwright's pinned builds of Chromium, Firefox and WebKit: system libs at provision time, browser binaries from the allowlisted Playwright + Chrome-for-Testing CDNs, devm CA trusted transparently by each browser's own trust store."
keywords: playwright e2e browser chromium firefox webkit headless testing browser-testing install-deps ms-playwright cdn
since: recipes-vNEXT
---

# Playwright

Run browser e2e inside the VM with Playwright's own pinned browser builds —
Chromium, Firefox, WebKit, or any subset. Two frictions: the browser CDNs
aren't allowlisted, and runtime `apt-get update` for browser libs is fragile
in the VM (broken outright under `docker: true`; see the docker recipe), so
the libs come from `packages:` at provision time.

## devm.yaml additions

```yaml
packages:
  # Chromium's runtime libs — the distro chromium's Depends chain pulls
  # nss/glib/x11/xkbcommon + fonts in one line instead of enumerating
  # ~20 t64 packages.
  - chromium
  # WebKit's libsoup/GnuTLS stack reads the system CA bundle through
  # p11-kit-trust (shipped by this package); without it, WebKit can't
  # see devm's CA at all. Omit if you don't run WebKit — Chromium's
  # NSS and Firefox's per-profile cert9.db (see "Project side" below)
  # both work without it.
  - p11-kit-modules

  # Firefox runtime libs — verbatim from Playwright's
  #   packages/playwright-core/src/server/registry/nativeDeps.ts
  # (debian13 → firefox). Re-sync when you bump @playwright/test:
  # trixie package names drift (the t64 transition, library SONAMEs).
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
    - cdn.playwright.dev                      # Firefox + WebKit CDN
    - playwright.download.prss.microsoft.com  # mirror fallback
    # @playwright/test ≥ 1.63 fetches the Chromium binary from
    # Chrome for Testing; keep this path-scoped — the bare bucket
    # host carries unrelated Google content.
    - storage.googleapis.com/chrome-for-testing-public/*

snapshots:
  # Playwright's browser cache (~1.5-2 GB for all three browsers +
  # headless-shell + ffmpeg). Captured at teardown and restored at
  # install so a recreate skips the CDN download; when @playwright/test
  # bumps, the cache goes stale and `playwright install` fetches the
  # new builds, which the next teardown captures in turn.
  pwcache:
    path: /home/devm/.cache/ms-playwright
    on_teardown: true
```
```bash
# devm.sh

# Restore first — on a recreate the snapshot repopulates the browser
# cache so `playwright install` is a no-op. On first boot there is no
# archive yet; restore exits 0 and the first `playwright install`
# does the CDN download.
install() {
  gdevm snapshot restore pwcache
}
```

Trust-bridge wiring happens project-side — see the `playwright.config.ts`
snippet below. No `startup()` symlink dance: Playwright patches Firefox's
enterprise-policy enforcement out of its test build, so the usual
install-dir `distribution/policies.json` route is silently ignored. The
portable Linux mechanism is the `security.enterprise_roots.enabled`
pref.

All three CDN hosts are required for `@playwright/test` 1.63+:
`playwright install` tries the Playwright CDN, then the Microsoft
mirror, and the Chromium binary itself comes from Chrome for Testing.
Allow one and the others still 403 (`devm denials` shows them).
`network.allow` is a live bucket (`devm reconcile`, no restart);
`packages:` is live too — devm apt-installs under the project's
current `network.allow`.

## Project side: pin `@playwright/test`, install browsers you need

```jsonc
// package.json
"devDependencies": { "@playwright/test": "1.65.1" },
"scripts": { "e2e:install": "playwright install chromium firefox webkit" }  // NO --with-deps
```

Run `e2e:install` once in the VM. `playwright install` downloads the
browser builds matching the installed `@playwright/test` version — pin
it so the builds are stable. Drop any browser you don't need:
`playwright install chromium` alone is the Chromium-only shape this
recipe shipped with originally. Skip `--with-deps`: it apt-installs at
runtime (the fragile path above) and the libs already came from
`packages:`.

Python playwright works identically — same CDN hosts, same libs; the
pin then lives in the Python lockfile and the command is `playwright
install chromium firefox webkit` from the venv.

If the project runs Firefox, seed a persistent Firefox profile with
devm's CA and launch Playwright against it:

```bash
# devm.sh (install step)
install() {
  gdevm snapshot restore pwcache
  mkdir -p /home/devm/.pw-profile-firefox
  certutil -N -d sql:/home/devm/.pw-profile-firefox --empty-password
  certutil -A -n devm -t "C,," \
    -d sql:/home/devm/.pw-profile-firefox \
    -i /opt/devm/ca/devm.crt
}
```

```ts
// your test file
import { test, expect, firefox } from '@playwright/test';
test('firefox against devm hairpin', async () => {
  const ctx = await firefox.launchPersistentContext(
    '/home/devm/.pw-profile-firefox',
  );
  const page = await ctx.newPage();
  await page.goto('https://api.myproject.test/');
  // ... assertions
  await ctx.close();
});
```

Why: Linux-Firefox has no OS-trust-store integration — no
`security.enterprise_roots.enabled` support on Linux, and Playwright
patches enterprise-policy file reading out of its test build. The
one portable trust mechanism is a per-profile NSS db seeded with
`certutil -A`, which `launchPersistentContext` then reuses. Chromium
and WebKit need neither the profile nor certutil: Chromium reads NSS
at `~/.pki/nssdb` (seeded by devm's `install.sh`), WebKit reads the
system CA bundle via GnuTLS.

Python-playwright projects pass the equivalent via
`firefox.launch_persistent_context('/home/devm/.pw-profile-firefox')`.

## Notes

- **Headless only.** No display server in the VM; run with the default
  headless mode (`xvfb` if a tool insists on a display).
- **Trust across browsers.** Chromium reads NSS at `~/.pki/nssdb`
  (seeded by devm's `install.sh` with the devm CA); nothing extra
  needed. Firefox needs a `certutil`-seeded persistent profile and
  `launchPersistentContext` as shown above — Linux-Firefox has no
  OS-trust-store integration and Playwright's test build has
  enterprise policies patched out, so per-profile NSS seeding is the
  one portable mechanism. WebKit's libsoup stack uses glib-networking
  → GnuTLS → p11-kit-trust → the system CA bundle directly, with no
  per-recipe config. One CA, three mechanisms, Firefox is the
  load-bearing one.
- **Trixie package-name drift.** Playwright's `nativeDeps.ts` is the
  source of truth; when you bump `@playwright/test`, re-sync the
  Firefox and WebKit blocks. `apt install` silently accepts an unknown
  name and marks it `un` (not installed) — a stale recipe fails at
  browser-launch time, not at install time.

## Verifying

```
npx playwright --version
npx playwright install chromium firefox webkit     # → downloads via the allowlisted CDN
node -e "const pw = require('@playwright/test');  \
  (async () => { \
    for (const b of ['chromium','firefox','webkit']) { \
      const br = await pw[b].launch(); console.log(b, await br.version()); await br.close(); \
    } \
  })()"
```

Verified end-to-end by `e2e/test_204_playwright_recipe.py` (`recipe`
marker; `just e2e-recipe test_204_playwright_recipe`), which cold-starts
with this recipe, serves a stdlib HTTPS hairpin page at
`https://app.<project>.e2e.test/`, and asserts that all three browser
projects (chromium + firefox + webkit) load the page, run its JS,
fetch `/data.json` over HTTPS and render `"yes"` into `#r`. Each
browser's trust store accepting iron-proxy's re-signed cert is what
the `3 passed` line proves.

Upstream: <https://playwright.dev/docs/browsers>
