---
name: tool/ai/camoufox
category: ai
display_name: Camoufox
description: Camoufox — anti-detect Firefox with runtime fingerprint spoofing, driven from Python; volume-backed to survive teardown.
keywords: camoufox firefox playwright scraping anti-detect fingerprint xvfb
since: recipes-v1.0.0
---

# Camoufox

[Camoufox](https://camoufox.com/) is a Firefox fork with runtime
fingerprint spoofing (screen, hardware, fonts, WebGL, timezone,
locale, …) driven from Python via its `pkgman` module. Any workload
in a devm VM that shells to `camoufox.launch_persistent_context`
(directly or via `AsyncCamoufox` / the sync `Camoufox` wrapper) needs:

1. an X server (Camoufox runs headed by default via `Xvfb`),
2. Firefox's runtime library set (chromium's deps aren't a superset),
3. a session bus (Firefox misbehaves without `dbus-launch`),
4. egress to two GitHub hosts to download the ~1.2 GB Firefox binary
   the first time `pkgman.fetch()` runs, and
5. a devm volume at the extraction path so subsequent boots don't
   re-download.

## devm.yaml additions

```yaml
packages:
  # Camoufox runtime prerequisites — Firefox needs different libs than
  # chromium, and `camoufox/virtdisplay.py` shells out to `which("Xvfb")`.
  - xvfb                # camoufox VirtualDisplay
  - libdbus-glib-1-2    # camoufox's bundled Firefox links it
  - libxt6t64           # Firefox needs libXt (trixie t64-transition
                        # rename of libxt6 — apt-cache only has libxt6t64)
  - dbus-x11            # dbus-launch — Firefox misbehaves with no session bus

network:
  allow:
    # Camoufox binary download. Two hops:
    #   1. Release page redirect (github.com)
    #   2. Actual asset CDN, keyed by camoufox's release-asset id 834082440
    - github.com/daijro/camoufox/releases/download/*
    - release-assets.githubusercontent.com/github-production-release-asset/834082440/*

volumes:
  # Where camoufox's pkgman.py extracts the ~1.2 GB Firefox bundle
  # (INSTALL_DIR-derived, XDG_CACHE_HOME unset — the path is stable).
  # A volume here keeps the binary teardown-durable so the fetch runs
  # once per Mac disk, not once per VM lifetime.
  camoufox: /home/devm/.cache/camoufox

startup:
  # Bridge devm's CA into camoufox's Firefox trust. devm 0.18.1+ drops
  # /etc/firefox/policies/policies.json with a SecurityDevices entry
  # pointing at p11-kit-trust.so; stock Firefox reads that path
  # directly. Camoufox reads its INSTALL_DIR/distribution/policies.json
  # instead — a symlink there routes it to the canonical file.
  # `mkdir -p` covers the first cold-start where distribution/ doesn't
  # yet exist; `ln -sfn` is idempotent so this is safe on every boot.
  - mkdir -p /home/devm/.cache/camoufox/distribution
  - ln -sfn /etc/firefox/policies/policies.json /home/devm/.cache/camoufox/distribution/policies.json
```

## Notes

- **`libxt6` doesn't exist on Debian trixie.** The t64 time_t transition
  renamed it to `libxt6t64` (same class of renames as `libasound2t64`,
  `libcups2t64`, `libncurses6t64`, etc.). A recipe that lists `libxt6`
  will silently fail on trixie — apt reports the package as `un`
  (unknown / not installed) even though a reconcile prints
  `+ package libxt6`. If the recipe supports pre-trixie bases too, list
  both and let apt pick.
- **The chromium package isn't a substitute for these Firefox libs.**
  If a project already installs `chromium` (e.g. for Playwright), that
  covers `libnss3` / `libgbm1` / `libasound2t64` / etc., but NOT
  `libdbus-glib-1-2` / `libxt6t64`. Camoufox's Firefox links to those
  and will crash on launch without them.
- **The two egress entries are load-bearing separately.** The
  `github.com/daijro/camoufox/releases/download/*` entry only reaches
  GitHub's release-page redirect; the actual `.tar.zst` download 302s
  to `release-assets.githubusercontent.com` with a signed URL under
  `/github-production-release-asset/834082440/...` — that entry has
  to be added or the fetch dies at the redirect. Asset ID `834082440`
  is stable across camoufox releases (it identifies the repo, not the
  version).
- **Path scoping is worth using here.** Both paths are narrow and
  stable, so leaving the asset id or the daijro org path bare is more
  egress surface than the recipe needs.
- **The extraction path is `~/.cache/camoufox`, not `~/.camoufox`.**
  Camoufox's `pkgman.py` derives `INSTALL_DIR` from
  `XDG_CACHE_HOME/camoufox`, defaulting to `~/.cache/camoufox` when
  `XDG_CACHE_HOME` is unset (the standard case in devm). Pointing a
  volume at `~/.camoufox` is a dead slot — nothing in camoufox writes
  there.
- **Fingerprint payloads have version drift, not just path drift.**
  A `browser_fingerprint` JSON captured against an older camoufox
  (e.g. anything shipping the `fontconfig/mac` addon subdir) needs
  the addon subdir rewritten to `fontconfig/macos` for the current
  build. Path-rewrite scripts that only substitute homedir prefixes
  will silently produce fingerprints that half-work. This isn't a
  devm concern per se, but worth flagging so recipe adopters aren't
  surprised.
- **First-launch download is ~1.2 GB.** Ride the open-egress
  provisioning window (i.e. call `pkgman.fetch()` from an `install:`
  or `startup:` script) if you don't want the runtime allowlist to
  carry the two GitHub hosts. Otherwise the recipe as written above
  keeps them on the runtime list.
- **HTTPS through iron-proxy just works** — including for hosts under
  Camoufox's control (Update Ping to `aus5.mozilla.org`, etc.) and
  any allowlisted app URL the workload hits. Every iron-proxy MITM
  cert is signed by devm's root CA, which is loaded into Firefox via
  the `startup:` symlink above (`p11-kit-trust.so` bridges the system
  CA store into NSS, so per-profile `certutil` dances aren't needed).
  Without the symlink you'll see `SEC_ERROR_UNKNOWN_ISSUER` on the
  first HTTPS fetch through iron-proxy — the recipe is complete only
  with both the packages and the startup step.

## Verifying

```
devm shell
$ command -v Xvfb dbus-launch                       # both present
$ dpkg -l libdbus-glib-1-2 libxt6t64 | grep '^ii'  # both installed
$ ls ~/.cache/camoufox/camoufox-bin                # binary present after first fetch
$ readlink ~/.cache/camoufox/distribution/policies.json
                                                    # → /etc/firefox/policies/policies.json
$ mount | grep camoufox                            # vol_camoufox bind-mounted
$ python -c "from camoufox.sync_api import Camoufox; \
    Camoufox(headless=True).__enter__().pages"     # smoke-launches Firefox
```

The `mount` line should show something like:

```
vol_camoufox on /home/devm/.cache/camoufox type virtiofs (rw,relatime)
```

confirming the volume is layered over the extraction path.

## After `devm teardown`

The camoufox volume backing lives Mac-side at
`~/Library/Application Support/devm/volumes/<project>/camoufox/` and
survives teardown. Next cold-start reattaches it; `pkgman.fetch()`
sees the binary is already present and skips the download.
