---
name: tool/ai/agent-browser
category: ai
display_name: agent-browser
description: Fast Rust CLI for in-session browsing (`open` / `snapshot -i` / `click @e1` / `fill @e2 …`). Reuses Playwright's chromium automatically — no second browser, no config. Prefer over ad-hoc Playwright scripts for "go look at this page" tasks.
keywords: agent-browser vercel headless browser chromium playwright agent claude
since: recipes-v1.0.0
---

# agent-browser

[agent-browser](https://github.com/vercel-labs/agent-browser) — a Rust
CLI the in-VM agent drives with structured commands and compact text
output. Auto-detects the Playwright chromium already on disk, so
installation is one `npm i -g` and there's no extra browser to
download.

Prereq: the Playwright recipe (`tool/tool/playwright`) — supplies
`chromium` runtime libs via `packages:` and the browser binary via
`playwright install chromium`.

## devm.yaml additions

```yaml
scripts:
  install-agent-browser:
    # fnm's `default` alias is on `path:` (per the Node recipe), so
    # `npm` is on PATH here in `install:` — no extra env setup needed.
    # Guard so the step is idempotent.
    - command -v agent-browser >/dev/null 2>&1 || npm install -g agent-browser

install:
  # After the Node + Playwright installs.
  - ">install-agent-browser"

network:
  allow:
    - registry.npmjs.org       # npm install -g agent-browser
```

Runtime egress: **navigation is subject to `network.allow`.**
`agent-browser open https://some-site` fails silently as a page error
if `some-site` isn't allowlisted — check `devm denials` when a page
won't load.

## Notes

- **Auto-detects Playwright's chromium.** With
  `~/.cache/ms-playwright/chromium-<rev>/chrome-linux/chrome` present
  (from `playwright install chromium`), `agent-browser open` uses it.
  No `AGENT_BROWSER_EXECUTABLE_PATH`, no `agent-browser install`, no
  extra CDN in the allowlist.
- **Separate profiles from Playwright by default.** State lives in
  `~/.agent-browser/`; Playwright uses `~/.cache/ms-playwright/`. Both
  read the same binary but hold their own user-data dirs, so
  Chromium's ProcessSingleton lock never collides. Rule: reuse the
  binary, never share a `--profile`/user-data-dir.
- **Rehydration.** No extra step — `install:` re-runs on the next
  fresh VM after teardown; Playwright's own recipe re-runs `playwright
  install chromium` alongside.

## Recommended CLAUDE.md line

Tell the in-VM agent:

```
- For "go look at this page" tasks, use agent-browser
  (`open <url>` → `snapshot -i` → `click @e1` / `fill @e2 "…"`)
  rather than ad-hoc Playwright/node scripts. Reuses the existing
  browser, compact output. Navigation hosts must be in
  network.allow.
```

## Verifying

```
$ agent-browser --version                                     # 0.27.0+
$ agent-browser open "data:text/html,<h1>ok</h1>"             # no egress
$ agent-browser snapshot -i                                   # heading "ok" [ref=e1]
$ find ~/.agent-browser ~/.cache/agent-browser -name chrome   # empty (reused Playwright)
```

Upstream: <https://github.com/vercel-labs/agent-browser>
