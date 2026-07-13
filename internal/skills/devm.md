---
name: devm
description: Configure and edit devm.yaml — a Mac+Tart-VM dev workspace tool with iron-proxy egress enforcement. Use when the user wants to set up devm in a project, add ports / services / env / install steps / mounts, integrate tools, or understand devm's process model.
---

# devm

## What devm is

devm is a brew-installed CLI for macOS Apple Silicon that provisions a per-project Tart VM as your development environment. Your project directory is mounted into the VM at its same absolute path as on the Mac. All outbound network traffic from the VM is gated through an iron-proxy daemon running on the Mac, so the VM cannot reach the internet except through an explicit allowlist. Configuration lives in `devm.yaml` at the project root.

## Three-process model

- **`devm` CLI** — the command you type in your terminal. Reads `devm.yaml`, renders `.devm/` from the current config, then talks to the service over a Unix socket to start or query the VM. Once the VM is up, it attaches your terminal via `tart exec`.
- **the devm daemon** (LaunchDaemon `com.devm.service`, managed via `devm service` subcommands) — owns the VM lifecycle (start, stop) and drives iron-proxy on the Mac. Secrets from the macOS login keychain are resolved CLI-side and handed to the daemon at start time, because the LaunchDaemon cannot access the login keychain directly.
- **The Tart VM** — runs your code on a Debian Linux base image. nftables inside the VM default-denies all outbound traffic, NATing port 80 and 443 to the Mac host. DNS inside the VM is handled by a local dnsmasq that forwards upstream queries to iron-proxy on the Mac. The VM has no direct path to the internet.

## Where the allowlist lives

`network.allow` in `devm.yaml` is the egress allowlist — each entry names a hostname (or `*` for open egress) your code may reach, and optionally declares which `!secret` values iron-proxy may inject on requests to that host. The list is enforced by iron-proxy on the Mac, which inspects each outbound request by SNI (TLS) or `Host` header (plain HTTP). Inside the VM, nftables permits only connections to the Mac host on iron-proxy's HTTP, HTTPS, and DNS ports, plus loopback. dnsmasq in the VM forwards all external DNS queries to iron-proxy's DNS port; iron-proxy returns a sentinel IP (RFC 5737 documentation-range address, never a real destination) for every resolved name, and the VM's nftables DNAT rules rewrite that sentinel's :80/:443 to iron-proxy's actual HTTP/HTTPS listen ports so all workload connections land at the Mac host and pass through the allowlist check before reaching the internet.

## Quickstart

```
brew install cirruslabs/cli/tart            # Tart is a prerequisite
brew install --cask mdubb86/tap/devm
devm install                                # registers the LaunchDaemon; requires sudo
devm shell                                  # cold-starts the VM, drops you in
```

## Where to look next

- `devm skills get schema` — every `devm.yaml` field, its type, and which change bucket it falls in.
- `devm skills get lifecycle` — when to use `devm shell`, `reconcile`, `stop`, `teardown`, and `validate`.
- `devm skills get service` — managing the background service (install, uninstall, restart, logs).
- `devm skills get routing` — how port declarations, the Caddy reverse-proxy, and `*.test` hostnames work inside the VM.
- `devm skills get secrets` — storing credentials in the macOS keychain and referencing them with `!secret` in `devm.yaml`.
- `devm skills get errors` — reading supervision error blocks and where logs live.
- `devm recipes get tool/service/docker` — docker is a built-in (`docker: true`), not a recipe you install, but the recipe covers the intricacies: the two egress paths (why `docker run` works with no config but `docker build` needs a Dockerfile RUN block), and the exact block to add for build-time HTTPS to survive iron-proxy's MITM.
