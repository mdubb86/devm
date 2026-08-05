---
name: tool/lang/node
category: lang
display_name: Node.js (via fnm)
description: Install Node.js via fnm (Fast Node Manager) — LTS by default, user-owned, `.nvmrc`/`.node-version` auto-switching, no shell integration required.
keywords: node nodejs fnm npm npx nvm version-manager
since: recipes-v1.0.0
---

# Node.js

Uses **fnm** — a single-binary Rust rewrite of nvm — installed fully as
the devm user (no root touches from fnm itself). devm.yaml owns the
PATH entry; no shell rc integration is needed, so scripts, services,
and interactive shells all see `node`/`npm`/`npx` uniformly.

## devm.yaml additions

```yaml
packages:
  - unzip                     # fnm install script downloads a zipped binary

scripts:
  install-fnm-and-node:
    # fnm — single-binary Node version manager. --skip-shell avoids
    # touching ~/.bashrc; devm.yaml `path:` below handles PATH.
    - curl -fsSL https://fnm.vercel.app/install | bash -s -- --skip-shell
    # Copy the user-owned binary to system PATH so `fnm` works from
    # every user + non-login shell + service unit.
    - sudo install -m 755 /home/devm/.local/share/fnm/fnm /usr/local/bin/fnm
    # Install the current LTS + mark it as default. The default alias
    # is a stable symlinked bin dir — the `path:` entry below tracks it.
    - fnm install --lts
    - fnm default lts-latest

install:
  - ">install-fnm-and-node"

path:
  # fnm's `default` alias always points at the active default's bin —
  # putting it on PATH makes `node`/`npm`/`npx` "just work" everywhere
  # (services, scripts, interactive shell) without any shell hook.
  - /home/devm/.local/share/fnm/aliases/default/bin

network:
  allow:
    - fnm.vercel.app                  # fnm install script host
    - github.com                      # fnm binary release
    - objects.githubusercontent.com   # release-artifact storage
    - nodejs.org                      # fnm's default Node distribution mirror
```

## Notes

- **fnm + node install as devm user** — `~/.local/share/fnm/` is
  user-owned end-to-end. Only `/usr/local/bin/fnm` is root-owned
  (that's the `sudo install` copy for system PATH). Node itself lives
  at `~/.local/share/fnm/node-versions/vX.Y.Z/installation/`.
- **No shell rc integration** — `--skip-shell` skips the bashrc
  modification fnm would normally add. devm's `path:` entry replaces
  what the shell hook would do. Services + scripts + shells all see
  the same PATH; nothing needs `source ~/.bashrc` to find node.
- **`.nvmrc` / `.node-version` compatibility** — fnm reads both. Drop
  a `.nvmrc` in your project and `fnm use` in that directory picks
  it up. (Auto-switch on `cd` is a shell-hook feature intentionally
  disabled here — call `fnm use` explicitly, or wire it into a
  project script if you want it.)
- **Upgrading Node** — `fnm install <version>` then
  `fnm default <version>`. The `default` alias flips and the `path:`
  entry picks up the new version without any devm.yaml change.
- **npm/npx** — installed alongside node by fnm; both on PATH via the
  same default alias.
- **Corepack** — disabled by default in fnm's install. If you use
  pnpm/yarn via corepack, enable per-project with
  `corepack enable pnpm` after node is installed.

Upstream: <https://github.com/Schniz/fnm>
