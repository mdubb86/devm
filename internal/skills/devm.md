---
name: devm
description: Configure and edit devm.yaml — a Mac+Tart-VM dev workspace tool with iron-proxy egress enforcement. Use when the user wants to set up devm in a project, add ports / services / env / install steps / mounts, integrate tools, or understand devm's process model.
---

# devm

## What devm is

devm is a brew-installed CLI for macOS Apple Silicon that provisions a per-project Tart VM as your development environment. Your project directory is mounted into the VM at its same absolute path as on the Mac. All outbound network traffic from the VM is gated through an iron-proxy daemon running on the Mac, so the VM cannot reach the internet except through an explicit allowlist. Configuration lives in `devm.yaml` at the project root.

## Three-process model

- **`devm` CLI** — the command you type in your terminal. Reads `devm.yaml`, renders `.devm/` from the current config, then talks to the daemon over a Unix socket to start or query the VM. Once the VM is up, it attaches your terminal to a shell inside the guest.
- **the devm daemon** — owns the VM lifecycle (start, stop) and drives iron-proxy on the Mac. Managed with `devm service`.
- **The Tart VM** — runs your code on a Debian Linux base image. It has no direct path to the internet: every outbound flow is intercepted on the Mac. Under the enforced egress policy, only allowlisted HTTPS hosts and NTP reach the outside; everything else is dropped.

## Where the allowlist lives

`network.allow` in `devm.yaml` is the egress allowlist — each entry names a hostname (or `*` for open egress) your code may reach, and optionally declares which `!secret` values iron-proxy may inject on requests to that host. Iron-proxy on the Mac inspects each outbound HTTP/HTTPS request by SNI (TLS) or `Host` header (plain HTTP) and consults `network.allow`. Matches are proxied through — with any declared `!secret` values injected on requests to that host — and non-matches are dropped with a diagnostic body the workload sees as a 502.

Iron-proxy also terminates TLS: it re-signs upstream certs with the devm CA, which is trusted inside the VM at first boot, so HTTPS to any allowlisted destination validates transparently.

Two projects that expose the same hostname (e.g. both use `api.test:443`) don't collide — each project's `*.test` DNS answer is that project's own address, and iron-proxy binds each project's listeners on that address independently.

## Quickstart

```
brew install cirruslabs/cli/tart            # Tart is a prerequisite
brew install --cask mdubb86/tap/devm
devm install                                # requires sudo
devm shell                                  # cold-starts the VM, drops you in
```

## Where to look next

- `devm skills get schema` — every `devm.yaml` field, its type, and which change bucket it falls in.
- `devm skills get lifecycle` — when to use `devm shell`, `reconcile`, `stop`, `teardown`, and `validate`.
- `devm skills get service` — managing the background service (install, uninstall, restart, logs).
- `devm skills get routing` — how port declarations, `devm route` commands, and `*.test` hostnames work on the Mac and inside the VM.
- `devm skills get secrets` — storing credentials in the macOS keychain and referencing them with `!secret` in `devm.yaml`.
- `devm skills get errors` — reading supervision error blocks and where logs live.
- `devm recipes get tool/service/docker` — docker is a built-in (`docker: true`), not a recipe you install, but the recipe covers the intricacies: the two egress paths (why `docker run` works with no config but `docker build` needs a Dockerfile RUN block), and the exact block to add for build-time HTTPS to survive iron-proxy's MITM.
