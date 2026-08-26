---
name: tool/lang/uv
category: lang
display_name: uv (Python)
description: "Run uv-based Python tooling inside a devm VM behind iron-proxy: pre-seed managed CPython in the open startup window so no GitHub host lands in the runtime allowlist, and keep .venv VM-local."
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

## devm.yaml additions (recommended: pre-seed at startup)

This is the default because it keeps the **runtime** allowlist minimal — the
CPython fetch happens in the boot-time open-egress window, so
`release-assets.githubusercontent.com` never needs to be a runtime host.

```yaml
env:
  UV_PYTHON_DOWNLOADS: manual  # blocks *implicit* runtime auto-fetches; explicit `uv python install` (preseed-python below) still works

scripts:
  install-uv:
    - curl -LsSf https://astral.sh/uv/install.sh -o /tmp/uv-install.sh
    - env UV_INSTALL_DIR=/home/devm/.local/bin INSTALLER_NO_MODIFY_PATH=1 sh /tmp/uv-install.sh
  # Runs in the OPEN-network startup window (before egress enforcement), so the
  # python-build-standalone download works without allow-listing GitHub release
  # assets. Idempotent — a fast no-op once the version is present. Reads the
  # project pin, defaults to 3.12.
  preseed-python:
    - /home/devm/.local/bin/uv python install "$(cat "$WORKSPACE/.python-version" 2>/dev/null || echo 3.12)"

install:
  - ">install-uv"        # recreate-bucket: uv binary, once per VM lifetime

startup:
  - ">preseed-python"    # restart-bucket: re-runs every boot in the open window (cheap when cached)

path:
  # login shells get ~/.local/bin via ~/.profile; scripts, services, and
  # `devm shell -- cmd` only see it with this entry
  - /home/devm/.local/bin
```

- `install:` (uv binary) is **recreate-bucket** — changing it needs a rebuild.
- `startup:` (the interpreter pre-seed) is **restart-bucket** — changing the pin
  takes effect on a plain `devm stop`/`start`, no teardown. It runs on every
  cold start; `uv python install <v>` is idempotent, so that's a fast no-op once
  the version is cached VM-local (`~/.local/share/uv`).

### Backup: runtime-download (generic GitHub egress)

If you'd rather not pin a version, or don't want a startup open-egress window on
every boot, drop `preseed-python` + `UV_PYTHON_DOWNLOADS` and let uv fetch
CPython on demand — the cost is one permanent GitHub host in the **runtime**
allowlist:

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
- **Version pin is a project concern.** The `3.12` default in `preseed-python`
  is just a fallback; real projects carry it in `.python-version`. (In BuzzTrack
  it's 3.12 because `datamodel-code-generator==0.69.0` rejects Debian 13's 3.13.)
- **Not in scope: telemetry noise.** The `DO_NOT_TRACK=1` / `SUPABASE_TELEMETRY=0`
  suggestion (to silence the posthog 403 stacktrace) is tool-agnostic and belongs
  in a devm-global default or the supabase recipe, not here.

## Verifying

```
devm shell -- uv --version   # non-login — proves the `path:` entry, not ~/.profile
devm shell
$ uv --version
$ uv python list                       # pre-seeded interpreter present, marked as managed
$ uv run python -c "import sys; print(sys.version)"
$ uvx datamodel-code-generator --version   # exercises rustls TLS through the proxy end-to-end
```

Verified live on uv 0.12.2 / Debian 13 arm64: the startup pre-seed downloads
CPython in the open window (no `release-assets` runtime host); uv's TLS
handshake through iron-proxy succeeds via the auto-set `UV_SYSTEM_CERTS=1`;
without the `path:` entry, non-login `uv` is `not found` (127).

Upstream: <https://docs.astral.sh/uv/> · network/CA docs:
<https://docs.astral.sh/uv/reference/environment/>
