---
name: devm
description: Configure and edit devm.yaml — a Mac+Tart-VM dev workspace tool with iron-proxy egress enforcement. Use when the user wants to set up devm in a project, add ports / services / env / install steps / volumes, integrate tools, or understand devm's process model.
---

# devm

## What devm is

devm is a brew-installed CLI for macOS Apple Silicon that provisions a per-project Tart VM as your development environment. The workspace inside the VM is hydrated via `git clone` from the repo(s) declared by `repos:` in `devm.yaml` — not a live bind mount of your Mac checkout. Its guest path mirrors the Mac cwd's absolute path string, kept in sync with the Mac side by a mutagen sync session; use `devm pop mac`/`devm pop vm` to open a file on the Mac side of that split. All outbound network traffic from the VM is gated through an iron-proxy daemon running on the Mac, so the VM cannot reach the internet except through an explicit allowlist. Configuration lives in `devm.yaml` at the project root.

## Three-process model

- **`devm` CLI** — the command you type in your terminal. Reads `devm.yaml`, then talks to the daemon over a Unix socket to start or query the VM. Once the VM is up, it attaches your terminal to a shell inside the guest.
- **the devm daemon** — owns the VM lifecycle (start, stop), runs its own built-in ProxyServer for `*.test` ingress on the Mac, and spawns per-project iron-proxy for egress enforcement. Managed with `devm service`.
- **The Tart VM** — runs your code on a Debian Linux base image. It has no direct path to the internet: every outbound flow is intercepted on the Mac. Under the enforced egress policy, only allowlisted HTTPS hosts and NTP reach the outside; everything else is dropped.

## Menu-bar app

**Menu-bar app (`/Applications/devm.app`).** A resident macOS menu-bar item; click it → "Open devm" → a graphical status dashboard shows every project's VM state, iron-proxy health, and approve-gate divergence. Refreshes every second while visible. Installed automatically by `devm install`. First launch shows the "unidentified developer" Gatekeeper warning; right-click → Open once to accept.

## Where the allowlist lives

`network.allow` in `devm.yaml` is the egress allowlist — each entry names a hostname (or `*` for open egress) your code may reach, and optionally declares which `!secret` values iron-proxy may inject on requests to that host. Iron-proxy on the Mac inspects each outbound HTTP/HTTPS request by SNI (TLS) or `Host` header (plain HTTP) and consults `network.allow`. Matches are proxied through — with any declared `!secret` values injected on requests to that host — and non-matches are dropped with a diagnostic body the workload sees as a 502.

Iron-proxy also terminates TLS: it re-signs upstream certs with the devm CA, which is trusted inside the VM at first boot, so HTTPS to any allowlisted destination validates transparently. Two trust stores are seeded per guest: the OpenSSL system bundle (`/usr/local/share/ca-certificates/devm.crt` + `update-ca-certificates`) — reached by curl, git, node, python, and every other toolchain that reads either the OpenSSL bundle or one of devm's ~12 CA env-var hooks (`SSL_CERT_FILE`, `CURL_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `PIP_CERT`, `CARGO_HTTP_CAINFO`, …) — and the devm user's per-user NSS db (`$HOME/.pki/nssdb`, seeded via `certutil` at install time) — reached by Chromium (both the Debian package and Playwright's Chrome-for-Testing), which validates against NSS and does not fall through to system NSS when the per-user db exists but lacks the cert.

Two projects that expose the same hostname (e.g. both use `api.test:443`) don't collide — each project's `*.test` DNS answer is that project's own address, and the daemon's built-in ProxyServer binds each project's listeners on that address independently.

## Quickstart

```
brew install cirruslabs/cli/tart            # Tart is a prerequisite
brew install --cask mdubb86/tap/devm
devm install                                # requires sudo
devm start                                  # cold-starts the VM
devm shell                                  # attaches, drops you in
```

## CLAUDE.md — host/guest split

A project's `CLAUDE.md` is git-tracked, so Mac and guest see the same content — each is its own clone of the same repo. Per-machine advice — anything Mac-only or guest-only — belongs in `CLAUDE.local.md` at the project root: Claude Code reads it alongside `CLAUDE.md`, and by convention it is gitignored per-user memory. devm's default mutagen sync excludes `CLAUDE.local.md`, so the Mac and guest carry independent copies.

Guest-specific setup — the `CLAUDE.local.md` stanza that tells Claude Code it is running in a devm guest, plus a set of guest-side skills that teach it how the environment works — ships as assets on the `tool/ai/claude` recipe. Run `devm recipes get tool/ai/claude` and follow its "Setup" section.

## `run <name>` — repo task dispatcher

Inside the guest, `run <name>` looks up `<name>` in the containing repo's `commands:` block (walks up from `$PWD` to find its repo) and runs it from the repo root. Two repos can define the same name; cwd picks the right one. `commands:` is just a list of function names, defined in the project's `devm.sh` (or `devm.me.sh`) — nothing fires automatically at cold-start on its own. To run something at cold-start, call it from `startup()` (every boot that opens the provisioning window) or `install()` (first boot only) in `devm.sh`.

### Mutagen transport

Mutagen syncs the workspace between Mac and guest via a Mac-side shim
(`cmd/tart-mutagen-ssh`, installed to `<daemon-runtime-dir>/mutagen-ssh-dir/ssh`)
that dispatches through `tart exec` (Tart's gRPC-over-vsock control
channel). No sshd is involved in the sync path. sshd remains for
interactive `ssh devm-<name>` and VS Code Remote-SSH.

### `gdevm` — guest dispatcher

`gdevm` is the only devm-owned binary inside the VM (the `devm` CLI never exists on the guest; `gdevm` never exists on the Mac). It reaches the Mac-side daemon over softnet.

- `gdevm pop <path-or-url>` — open a file with its default Mac app; same behavior as `devm pop mac`/`vm` from the other side of the boundary.
- `gdevm propose [--reason <text>] [--kind <file>]` — signal that a `devm.yaml` / `devm.me.yaml` / `devm.sh` / `devm.me.sh` edit is ready for Mac-side review.
- `gdevm run <command>` — invoke a named function from the project's guest command manifest (same lookup as `run <name>`).
- `gdevm passthrough --reason <text> [--for <duration>]` — request a supervised egress passthrough window; a human on the Mac authorizes it with `devm passthrough approve` (or `deny`).
- `gdevm upgrade` — pull the daemon's current `gdevm` binary, env template, and commands manifest into this VM.
- `gdevm recipes list | get <name> | asset ls <name> | asset get <name> <path>` — query the Mac-side recipes catalog from the guest.

## Propose channel

**Propose channel.** `devm propose` runs on either the Mac or the
guest; it signals the daemon that a human-approved config file has
been edited and is ready for review. Four files carry this contract:
`devm.yaml`, `devm.me.yaml`, `devm.sh`, `devm.me.sh`. Each lives on the
Mac under `~/Library/Application Support/devm/<name>/` and is synced
bidirectionally to the same filename under `/home/devm/` in the guest.
The approve gate refuses
`devm reconcile`/`devm start` when any of the four has changed since
the last approval, until the human approves the change.

## Passthrough egress — Mac verbs

Iron-proxy stays in the request path (MITM, audit, secret substitution) during a passthrough window; the allowlist check is what gets bypassed. The window auto-restores when its timer fires.

- `devm passthrough open [duration]` — open a window immediately (default 30s); a Go duration argument extends or shortens it.
- `devm passthrough close` — end an active window early and restore RESTRICTED egress.
- `devm passthrough approve` — consume a pending `gdevm passthrough` request and open the window with that request's duration.
- `devm passthrough deny` — clear a pending `gdevm passthrough` request without opening a window.

The guest side of this flow (agent-authored `gdevm passthrough --reason "..."`) is covered by the `tool/ai/claude` recipe's guest skills.

## Where to look next

- `devm skills get schema` — every `devm.yaml` field, its type, and which change bucket it falls in.
- `devm skills get lifecycle` — when to use `devm shell`, `devm start`, `reconcile`, `stop`, `teardown`, and `validate`.
- `devm skills get service` — managing the background service (install, uninstall, restart, logs).
- `devm skills get routing` — how port declarations, `devm route` commands, and `*.test` hostnames work on the Mac and inside the VM.
- `devm skills get secrets` — storing credentials in the on-disk secret store and referencing them with `!secret` in `devm.yaml`.
- `devm skills get errors` — reading supervision error blocks and where logs live.
- `devm pop mac <path-or-url>` — open a Mac-native file with its default app; refuses paths that resolve into a devm-managed volume. An `http://` / `https://` URL routes straight to the default browser. Paths outside any mirror (e.g. `/tmp/site/index.html`, `/var/log/foo.log`) get a live-sync session into a Mac scratch dir under `<runtime-dir>/pop-tmp/<id>/`. Subsequent guest edits propagate; the session self-terminates after 1h of no propagated change. Session count and oldest age surface in `devm status`. From the guest, `gdevm pop <path-or-url>` does the same thing from the other side of the boundary — see the `gdevm-guest` skill shipped by the `tool/ai/claude` recipe.
- `devm pop vm <path-or-url>` — open a file from the project's guest workspace (a `$WORKSPACE`-anchored path) with its default app on the Mac. An `http://` / `https://` URL routes straight to the default browser. Paths outside any mirror get the same live-sync session behavior as `devm pop mac`.
- `devm recipes get tool/service/docker` — docker is a built-in (`docker: true`), not a recipe you install, but the recipe covers the intricacies: the two egress paths (why `docker run` works with no config but `docker build` needs a Dockerfile RUN block), and the exact block to add for build-time HTTPS to survive iron-proxy's MITM.
