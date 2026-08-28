---
name: devm
description: Configure and edit devm.yaml — a Mac+Tart-VM dev workspace tool with iron-proxy egress enforcement. Use when the user wants to set up devm in a project, add ports / services / env / install steps / volumes, integrate tools, or understand devm's process model.
---

# devm

## What devm is

devm is a brew-installed CLI for macOS Apple Silicon that provisions a per-project Tart VM as your development environment. The workspace inside the VM is a devm-managed volume, hydrated via `git clone` from the repo declared by `repo:` in `devm.yaml` — not a live bind mount of your Mac checkout. Its guest path mirrors the Mac cwd's absolute path string, but the two are separate git clones; use `devm pop mac`/`devm pop vm` to open a file on the Mac side of that split. All outbound network traffic from the VM is gated through an iron-proxy daemon running on the Mac, so the VM cannot reach the internet except through an explicit allowlist. Configuration lives in `devm.yaml` at the project root.

## Three-process model

- **`devm` CLI** — the command you type in your terminal. Reads `devm.yaml`, then talks to the daemon over a Unix socket to start or query the VM. Once the VM is up, it attaches your terminal to a shell inside the guest.
- **the devm daemon** — owns the VM lifecycle (start, stop), runs its own built-in ProxyServer for `*.test` ingress on the Mac, and spawns per-project iron-proxy for egress enforcement. Managed with `devm service`.
- **The Tart VM** — runs your code on a Debian Linux base image. It has no direct path to the internet: every outbound flow is intercepted on the Mac. Under the enforced egress policy, only allowlisted HTTPS hosts and NTP reach the outside; everything else is dropped.

## Where the allowlist lives

`network.allow` in `devm.yaml` is the egress allowlist — each entry names a hostname (or `*` for open egress) your code may reach, and optionally declares which `!secret` values iron-proxy may inject on requests to that host. Iron-proxy on the Mac inspects each outbound HTTP/HTTPS request by SNI (TLS) or `Host` header (plain HTTP) and consults `network.allow`. Matches are proxied through — with any declared `!secret` values injected on requests to that host — and non-matches are dropped with a diagnostic body the workload sees as a 502.

Iron-proxy also terminates TLS: it re-signs upstream certs with the devm CA, which is trusted inside the VM at first boot, so HTTPS to any allowlisted destination validates transparently. Two trust stores are seeded per guest: the OpenSSL system bundle (`/usr/local/share/ca-certificates/devm.crt` + `update-ca-certificates`) — reached by curl, git, node, python, and every other toolchain that reads either the OpenSSL bundle or one of devm's ~12 CA env-var hooks (`SSL_CERT_FILE`, `CURL_CA_BUNDLE`, `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `PIP_CERT`, `CARGO_HTTP_CAINFO`, …) — and the devm user's per-user NSS db (`$HOME/.pki/nssdb`, seeded via `certutil` at install time) — reached by Chromium (both the Debian package and Playwright's Chrome-for-Testing), which validates against NSS and does not fall through to system NSS when the per-user db exists but lacks the cert.

Two projects that expose the same hostname (e.g. both use `api.test:443`) don't collide — each project's `*.test` DNS answer is that project's own address, and the daemon's built-in ProxyServer binds each project's listeners on that address independently.

## Quickstart

```
brew install cirruslabs/cli/tart            # Tart is a prerequisite
brew install --cask mdubb86/tap/devm
devm install                                # requires sudo
devm shell                                  # cold-starts the VM, drops you in
```

## CLAUDE.md — host/guest split

A project's `CLAUDE.md` is git-tracked, so both the Mac and the VM see the same content — each is its own clone of the same repo. Add this bullet to the project's `CLAUDE.md` when configuring devm, so a guest-side agent knows to consult the guest-specific reference first:

- **In devm guest (`$IS_SANDBOX=1`), read `/opt/devm/GUEST.md` first — commands here that name `devm`/`tart`/`just`/`brew`/`launchctl` are Mac-only.**

`/opt/devm/GUEST.md` is installed on every guest by devm's bundle installer (no user action needed). It covers the guest's view of the network, filesystem quirks (including the Node `fs.cpSync` gotcha on virtiofs volumes), lifecycle actions, and where to look when something breaks.

## Where to look next

- `devm skills get schema` — every `devm.yaml` field, its type, and which change bucket it falls in.
- `devm skills get lifecycle` — when to use `devm shell`, `reconcile`, `stop`, `teardown`, and `validate`.
- `devm skills get service` — managing the background service (install, uninstall, restart, logs).
- `devm skills get routing` — how port declarations, `devm route` commands, and `*.test` hostnames work on the Mac and inside the VM.
- `devm skills get secrets` — storing credentials in the on-disk secret store and referencing them with `!secret` in `devm.yaml`.
- `devm skills get errors` — reading supervision error blocks and where logs live.
- `devm pop mac <path>` — open a Mac-native file with its default app; refuses paths that resolve into a devm-managed volume.
- `devm pop vm <path>` — open a file from the project's guest workspace (a `$WORKSPACE`-anchored path) with its default app on the Mac.
- `.vm/` — a symlink at the Mac project root pointing at the primary volume's Mac-side storage; browse the live guest checkout without leaving the Mac.
- `devm recipes get tool/service/docker` — docker is a built-in (`docker: true`), not a recipe you install, but the recipe covers the intricacies: the two egress paths (why `docker run` works with no config but `docker build` needs a Dockerfile RUN block), and the exact block to add for build-time HTTPS to survive iron-proxy's MITM.
