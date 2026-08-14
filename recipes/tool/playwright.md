---
name: tool/tool/playwright
category: tool
display_name: Playwright
description: "Browser e2e inside the VM with Playwright's pinned browser: system libs at provision time via the distro chromium's Depends, browser binary from the allowlisted Playwright CDN, version-matched to the project's @playwright/test."
keywords: playwright e2e browser chromium headless testing browser-testing install-deps ms-playwright cdn
since: recipes-vNEXT
---

# Playwright

Run browser e2e inside the VM with Playwright's own pinned browser build
(Chrome for Testing). Two frictions: the browser CDN isn't allowlisted, and
the browser's system libs are best installed at provision time — runtime
`apt-get update` is fragile in the VM (broken outright under `docker: true`;
see the docker recipe).

## devm.yaml additions

```yaml
packages:
  # Chrome-for-Testing's runtime libs (libnss3, libgbm1, libasound2t64,
  # libatk*, libcups2t64, libxkbcommon0, …) + fonts, pulled in via the distro
  # chromium's Depends — one line instead of enumerating ~20 t64 packages.
  # (`playwright install-deps --dry-run` on Debian trixie lists only
  # fonts/xvfb and omits the core libs — not a usable source.)
  - chromium

network:
  allow:
    - cdn.playwright.dev                      # browser-binary CDN
    - playwright.download.prss.microsoft.com  # Microsoft mirror fallback
```

Both hosts are required: `playwright install` tries the primary CDN, then the
mirror — allow one and the other still 403s (`devm denials` shows them).
`network.allow` is a live bucket (`devm reconcile`, no restart); `packages:`
is recreate-bucket.

## Project side: pin `@playwright/test`, install browser-only

```jsonc
// package.json
"devDependencies": { "@playwright/test": "1.62.1" },
"scripts": { "e2e:install": "playwright install chromium" }  // NO --with-deps
```

Run `e2e:install` once in the VM. `playwright install` downloads the browser
build matching the installed `@playwright/test` version — pin it so the build
is stable. Skip `--with-deps`: it apt-installs at runtime (the fragile path
above) and the libs already came from `packages:`.

Python playwright works identically — same two CDN hosts, same libs; the pin
then lives in the Python lockfile and the command is `playwright install
chromium` from the venv.

## Notes

- **The browser cache is VM-local.** `~/.cache/ms-playwright` (~1 GB:
  chromium + headless-shell + ffmpeg) is wiped by `devm teardown`; re-run
  `e2e:install` after a rebuild (the libs persist via `packages:`).
  Alternatives if the re-download hurts:
  - **Provision-time install** — `npx --yes playwright@<pin> install chromium`
    as an `install:` step. Runs in the open-egress window, so both CDN hosts
    come off the runtime allowlist; the cost is a second copy of the version
    pin in devm.yaml.
  - **Volume the cache** — `volumes: { pwcache: /home/devm/.cache/ms-playwright }`.
    Survives teardown, but ~1 GB, and it goes stale against a bumped
    `@playwright/test` (a stale cache just triggers a re-download, so it only
    helps while the pin is unchanged).
- **Headless only.** No display server in the VM; run with the default
  headless mode (`xvfb` if a tool insists on a display).

## Verifying

```
npx playwright --version
npx playwright install chromium     # → downloads via the allowlisted CDN
node -e "const {chromium} = require('@playwright/test'); chromium.launch().then(async b => { console.log(await b.version()); await b.close(); })"
```

Verified live on devm 0.11.0 / Debian trixie arm64, `@playwright/test` 1.62.1
(Chrome for Testing 151.0.7922.34): both CDN hosts confirmed via `devm
denials`; `packages: [chromium]` supplied every runtime lib (a subsequent
`playwright install-deps` was a no-op); headless launch smoke passed.

Upstream: <https://playwright.dev/docs/browsers>
