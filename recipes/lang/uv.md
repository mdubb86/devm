---
name: tool/lang/uv
category: lang
display_name: uv (Python)
description: "Run uv-based Python tooling inside a devm VM behind iron-proxy: pre-seed managed CPython at the `commands` stage (open egress, workspace hydrated) so no GitHub host lands in the runtime allowlist, and keep .venv VM-local."
keywords: uv python rustls cpython venv datamodel-code-generator pip iron-proxy tls
since: recipes-vNEXT
---

# uv (Python)

Run [uv](https://docs.astral.sh/uv/) — `uv run`, `uvx`, `uv python`, and the
tools they drive (`datamodel-code-generator`, `ruff`, etc.) — inside a devm VM.
uv itself installs fine; the friction is one environment fact that isn't a code
bug.

**uv's managed-CPython download needs a non-obvious host.** `uv python install`
/ `uvx --python <v>` fetch python-build-standalone from GitHub **release
assets** (`release-assets.githubusercontent.com`), which `github.com` /
`objects.githubusercontent.com` / `raw.githubusercontent.com` do **not** cover,
and whose redirect carries a signed URL that can't be predicted.

## devm.yaml additions (recommended: pre-seed at `commands`)

Pre-seed via a `repos.<repo>.commands` entry marked `startup: true`: it fires
at the `commands` stage, after `repo-clone` has hydrated the workspace and
while iron-proxy is still in `passthrough`. `.python-version` is read from
the repo's guest cwd — no `$WORKSPACE` guess, no silent fallback, and no
runtime allowlist host needed.

```yaml
env:
  UV_PYTHON_DOWNLOADS: manual  # blocks *implicit* runtime auto-fetches; explicit `uv python install` (below) still works

scripts:
  install-uv:
    - curl -LsSf https://astral.sh/uv/install.sh -o /tmp/uv-install.sh
    - env UV_INSTALL_DIR=/home/devm/.local/bin INSTALLER_NO_MODIFY_PATH=1 sh /tmp/uv-install.sh

install:
  - ">install-uv"

repos:
  main:
    url: git@github.com:you/your-project.git
    commands:
      preseed-python:
        # Reads `.python-version` from the repo's guest cwd. Runs at the
        # `commands` stage — after `repo-clone`, still under iron-proxy's
        # passthrough authority — so no runtime allowlist host is needed.
        # No fallback: a project missing `.python-version` fails loud
        # instead of getting a silently-substituted 3.12 that surfaces
        # later as a confusing `uv sync` mismatch.
        exec: /home/devm/.local/bin/uv python install "$(cat .python-version)"
        startup: true

path:
  # login shells get ~/.local/bin via ~/.profile; scripts, services, and
  # `devm shell -- cmd` only see it with this entry
  - /home/devm/.local/bin
```

`uv python install <v>` is idempotent; the cached interpreter lives VM-local
under `~/.local/share/uv`, so the `commands`-stage pre-seed is a fast no-op
on every boot after the first.

### Backup: runtime-download (generic GitHub egress)

If the project has no `.python-version`, or you'd rather not run an open-egress
pre-seed on every boot, drop the `preseed-python` command + `UV_PYTHON_DOWNLOADS`
and let uv fetch CPython on demand — the cost is one permanent GitHub host in
the **runtime** allowlist:

```yaml
network:
  allow:
    # uv managed CPython — astral-sh/python-build-standalone release assets only
    - release-assets.githubusercontent.com/github-production-release-asset/162334160/*
```

## The `.venv`

A project `.venv/` is **per-system compiled tooling**, not portable config.
It lives in the volume, out of the way of your Mac's own clone if you have one.

- uv's **cache** (`~/.cache/uv`) is already VM-local.
- `UV_PROJECT_ENVIRONMENT=<VM-local path>` is an alternative (a live env change,
  no recreate) that pushes the venv fully outside the tree — use it only if your
  tooling drives everything through `uv run` rather than a hardcoded `./.venv`.

## The `SSL_CERT_FILE` trap (do NOT do this)

The obvious-looking fix — `SSL_CERT_FILE=/usr/local/share/ca-certificates/devm.crt`
— is worse than the bug. It **replaces** the trust set with a single cert, so
every host iron-proxy *passes through* (rather than MITMs) then fails. If Python
tooling (pip / requests / httpx / certifi consumers) needs a CA env var, point
it at the **merged bundle**, never the lone cert:

```yaml
env:
  SSL_CERT_FILE:      /etc/ssl/certs/ca-certificates.crt
  REQUESTS_CA_BUNDLE: /etc/ssl/certs/ca-certificates.crt
```

(For uv specifically you don't need these — devm sets `UV_SYSTEM_CERTS=1` in
`/etc/environment` since v0.9.6, which already reads that merged store.
`UV_NATIVE_TLS` is the deprecated spelling of the same flag; uv warns on it.)

## Notes

- **`UV_SYSTEM_CERTS=1` is auto-set.** devm ships it in `/etc/environment`
  since v0.9.6 so uv (Rust/rustls, ignores the OpenSSL trust store) reads
  the merged system CA bundle at `/etc/ssl/certs/ca-certificates.crt` where
  devm has installed its CA. Reaches every guest process (SSH, systemd, and
  `devm exec`/`devm shell` via the wrapper's `set -a; . /etc/environment`
  since v0.9.10). No project config needed.
- **`python3` apt package is optional.** uv manages its own interpreters; only
  add `python3` to `packages:` if the project needs a *system* python for other
  reasons.
- **`.python-version` is required.** No fallback in the recipe: a missing
  file fails loud (`cat: .python-version: No such file` → `uv python install
  ""` errors) rather than silently seeding a stale default. Pin the version
  the project actually needs.
- **Not in scope: telemetry noise.** The `DO_NOT_TRACK=1` / `SUPABASE_TELEMETRY=0`
  suggestion (to silence the posthog 403 stacktrace) is tool-agnostic and belongs
  in a devm-global default or the supabase recipe, not here.

## Verifying

```
devm start                   # picks up the new install: step
devm shell -- uv --version   # non-login — proves the `path:` entry, not ~/.profile
devm shell
$ uv --version
$ uv python list                       # pre-seeded interpreter present, marked as managed
$ uv run python -c "import sys; print(sys.version)"
$ uvx datamodel-code-generator --version   # exercises rustls TLS through the proxy end-to-end
```

Verified live on uv 0.12.2 / Debian 13 arm64: the `commands`-stage pre-seed
downloads CPython in the open-egress window (no `release-assets` runtime
host); uv's TLS handshake through iron-proxy succeeds via the auto-set
`UV_SYSTEM_CERTS=1`; without the `path:` entry, non-login `uv` is
`not found` (127).

Upstream: <https://docs.astral.sh/uv/> · network/CA docs:
<https://docs.astral.sh/uv/reference/environment/>
