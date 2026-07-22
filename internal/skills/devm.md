---
name: devm
description: Configure and edit devm.yaml — a Mac+Tart-VM dev workspace tool with iron-proxy egress enforcement. Use when the user wants to set up devm in a project, add ports / services / env / install steps / mounts, integrate tools, or understand devm's process model.
---

# devm

## What devm is

devm is a brew-installed CLI for macOS Apple Silicon that provisions a per-project Tart VM as your development environment. Your project directory is mounted into the VM at its same absolute path as on the Mac. All outbound network traffic from the VM is gated through an iron-proxy daemon running on the Mac, so the VM cannot reach the internet except through an explicit allowlist. Configuration lives in `devm.yaml` at the project root.

## Three-process model

- **`devm` CLI** — the command you type in your terminal. Reads `devm.yaml`, renders `.devm/` from the current config, then talks to the service over a Unix socket to start or query the VM. Once the VM is up, it attaches your terminal via `tart exec`.
- **the devm daemon** (LaunchDaemon `com.devm.service`, managed via `devm service` subcommands) — owns the VM lifecycle (start, stop) and drives iron-proxy on the Mac. Secrets from the macOS login keychain are resolved CLI-side and handed to the daemon at start time, because the LaunchDaemon cannot access the login keychain directly. A companion root helper (`com.devm.helper`) does the small privileged tasks the unprivileged daemon can't — binding low ports (:80/:443) on the per-project loopback IPs and handing the file descriptors back over a Unix socket via `SCM_RIGHTS`.
- **The Tart VM** — runs your code on a Debian Linux base image. The VM has no NIC of its own that reaches your LAN: `tart run --net-softnet` spawns a userspace network stack on the Mac (gvisor-tap-vsock) that terminates the guest's virtio NIC over vsock. Every guest packet is handed to this **softnet** process on the Mac, which decides — per its live egress policy — whether to drop it, forward it as-is, or redirect :80/:443 to iron-proxy. There is no guest-side outbound firewall in the enforcement path: the base image ships an nftables lock that gates the boot until provisioning starts, and the first thing provisioning does is `nft flush ruleset`. From that point on, softnet is the sole egress boundary.

## Where the allowlist lives

`network.allow` in `devm.yaml` is the egress allowlist — each entry names a hostname (or `*` for open egress) your code may reach, and optionally declares which `!secret` values iron-proxy may inject on requests to that host. Enforcement happens on the Mac at two layers:

1. **softnet** (Mac userspace, per-VM) receives every outbound guest flow over vsock. Under the enforced policy, TCP :80 and :443 are the only ports that leave — they're forwarded to that project's iron-proxy listeners on the Mac's `127.42.0.N` per-project loopback IP. Every other TCP port is dropped (RST); UDP :123 is forwarded to devm's SNTP responder so the guest clock stays in sync with the Mac. DNS from the guest terminates at softnet's own gateway:53 resolver, which under enforcement forwards to iron-proxy's DNS.
2. **iron-proxy** (Mac userspace, per-project) inspects each :80/:443 flow by SNI (TLS) or `Host` header (plain HTTP) and consults `network.allow`. Matches are proxied through — with any declared `!secret` values injected on requests to that host — and non-matches are dropped with a diagnostic body the workload sees as a 502. iron-proxy is also the CA-signing terminator for TLS: it re-signs upstream certs with the devm CA so the guest's system trust store validates HTTPS to any allowlisted destination transparently.

Every running project gets its own `127.42.0.N` address (pool `127.42.0.1..20` for prod, one project per address) so two projects that both expose e.g. `api.test:443` never collide — each project's `api.test` DNS answer is its own `127.42.0.N` and each project's iron-proxy binds only on that address.

## Quickstart

```
brew install cirruslabs/cli/tart            # Tart is a prerequisite
brew install --cask mdubb86/tap/devm
devm install                                # registers the LaunchDaemon; requires sudo
devm shell                                  # cold-starts the VM, drops you in
```

`devm install` prompts for Touch ID / password twice — once for the main LaunchDaemon + resolver + CA trust + `_devm` group creation + lo0 pool aliases, and once (silently, immediately after) for the root helper LaunchDaemon that binds the low ports on the pool IPs.

## Where to look next

- `devm skills get schema` — every `devm.yaml` field, its type, and which change bucket it falls in. Includes `scripts:` for reusable multi-command shell snippets referenced from `install:`/`startup:`.
- `devm skills get lifecycle` — when to use `devm shell`, `reconcile`, `stop`, `teardown`, and `validate`.
- `devm skills get service` — managing the background service (install, uninstall, restart, logs).
- `devm skills get routing` — how port declarations, per-project loopback IPs, and `*.test` hostnames work on the Mac and inside the VM.
- `devm skills get secrets` — storing credentials in the macOS keychain and referencing them with `!secret` in `devm.yaml`.
- `devm skills get errors` — reading supervision error blocks and where logs live.
- `devm recipes get tool/service/docker` — docker is a built-in (`docker: true`), not a recipe you install, but the recipe covers the intricacies: the two egress paths (why `docker run` works with no config but `docker build` needs a Dockerfile RUN block), and the exact block to add for build-time HTTPS to survive iron-proxy's MITM.
