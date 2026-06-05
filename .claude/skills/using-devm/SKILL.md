---
name: using-devm
description: Use when working in a project that uses devm. Tells you what devm.yaml controls, how `devm shell` / `devm reconcile` / `devm stop` behave under the anchor-alive contract, what changes require recreate vs apply live, and where to look when something flakes.
---

# Using devm

devm wraps Docker Sandboxes (`sbx`) with a declarative `devm.yaml`. The file describes a sandbox; devm renders it to an sbx kit and orchestrates the lifecycle. Source of truth for behavior is `/Users/michael/workspace/devm/internal/`.

## Binary

Assume `devm` is on PATH. Verify with `devm --help` (or `which devm`). If it's missing, the user installs it; this skill doesn't cover devm's own install path.

## Commands

| Command | What it does |
|---|---|
| `devm shell` | Cold-start (if needed) and attach. Anchor stays alive after shell exit. |
| `devm reconcile` | Diff `devm.yaml` against the last-applied snapshot; preview changes. Exit 2 on non-TTY recreate. |
| `devm reconcile --yes` | Apply the diff. LIVE changes happen in-place; TEARDOWN changes do `sbx rm` + re-create. |
| `devm stop` | Stop the sandbox (preserves VM state). Required to actually stop — shell exit alone won't. |
| `devm stop --yes` | Skip the confirm prompt. |
| `devm teardown` | `sbx rm` — destroys the VM and all installed state. |
| `devm status` | Show sandbox state, mappings, etc. |

## devm.yaml — minimum viable

```yaml
project:
  id: myproj              # required, used as agent name
  sandbox_name: myproj-sbx # required, the sbx sandbox name
  hostname_apex: myproj.local  # required field, exposed to templates as
                               # {{.Project.HostnameApex}}. NOT used by
                               # Caddy or anywhere else in devm — just a
                               # template variable. Convention: <id>.local.
  port_offset: 51000      # port + port_offset = host_port

base_image:
  docker: false           # true → docker-in-docker base

install:
  - apt-get update && apt-get install -y jq

services:
  api:
    port: 8080            # in-VM listen port; host port = 51000 + 8080 = 59080
                          # By default the mapping binds to 127.0.0.1
                          # (localhost-only). To expose on the LAN, write the
                          # SAME field as a string with the bind interface
                          # prefix: `port: "0.0.0.0:8080"`. The trailing port
                          # in the string must equal the sandbox port; the host
                          # port is still port_offset + port — devm guarantees
                          # that, and the bind portion controls only the
                          # interface.
                          #
                          # Examples:
                          #   port: 8080                  → 127.0.0.1:59080:8080
                          #   port: "0.0.0.0:8080"        → 0.0.0.0:59080:8080
                          #   port: "192.168.1.10:8080"   → 192.168.1.10:59080:8080
    env:                  # service env vars; exposed as API_KEY=...
      LOG_LEVEL: info
    env_inject: true      # also inject API_PORT and API_HOST
    env_host: 0.0.0.0
    startup:
      - command: ["sh", "-c", "node server.js"]
        background: true  # devm renders this as foreground+nohup&

  worker:
    startup:
      - command: ["sh", "-c", "while true; do work; sleep 5; done"]
        background: true

env:                      # project-level env vars (visible in every shell)
  EDITOR: vim

network:
  allowed_domains:        # sbx DNS allowlist — HOST-GLOBAL, not per-sandbox
    - github.com
    - api.openai.com

mounts:                   # additional host paths, mounted at the same path in VM
  - ~/.aws:ro             # mirrored to /Users/michael/.aws inside the VM
  - ../sibling-project    # rel paths resolve against devm.yaml dir
```

## Anchor-alive contract (the key behavior to internalize)

- `devm shell` spawns `nohup sbx run …` as the **anchor** and never kills it on the normal path.
- Anchor outlives the user shell. Sandbox stays running when the user exits the shell.
- To stop the sandbox: `devm stop` (calls `sbx stop NAME`, which reaps the anchor).
- To destroy: `devm teardown` (calls `sbx rm`).
- Anchor outlives terminal close because of the `nohup` SIGHUP=IGN inheritance.

If you spin up `devm shell`, then close the terminal, then open a new terminal and run `devm shell` again, you'll **attach to the still-running sandbox** (warm path). This is intentional.

## Reconcile buckets — what's safe to live-change

Knowing the bucket tells you whether changing a field needs a full recreate.

**LIVE (no restart):**
- Service `port` add/remove/change (port reconcile)
- Service `env`, project `env` add/remove/change (picked up by next `devm shell`)
- `network.allowed_domains` ADD (sbx policy is global; remove is STOP-bucket)
- Template content (`services.*.templates`) — re-renders + re-installs in-VM

**STOP+SHELL (sbx stop, re-attach):**
- Service `startup` change
- `network.allowed_domains` REMOVE

**TEARDOWN+SHELL (`sbx rm` + cold start):**
- `install` (re-runs at create)
- `base_image.docker` toggle
- `project.*` identity fields
- `services.*.masks` (volume mounts baked at create)
- `mounts` (positional workspaces baked at create)

Authoritative source: `internal/orchestrator/diff.go` `changeBucket` map.

## Debugging

When something flakes or behaves surprisingly:

1. **First check `docs/sbx-quirks.md`** — every known upstream sbx quirk is listed with its workaround and the regression test that pins it. Most likely you're hitting #6 (publish phantom, resolved), #5 (5s daemon kill on anchor death, resolved by anchor-alive), or #1 (`sbx exec` stdin pipe hang, base64 workaround).

2. **Enable debug logging:** `DEVM_DEBUG=shell devm shell` (category-gated). Categories: `shell`, `ports`. `DEVM_DEBUG=shell,ports` for both. `DEVM_DEBUG=all` for everything.

3. **Capture pexpect buffer in e2e tests:** if you're writing an e2e test that hits an unexpected `EOF`, print `sh._child.before` to see the full devm output.

4. **`sbx ls` and `sbx ports NAME --json`** are the ground truth for sandbox state.

## Test discipline

The e2e suite lives in `e2e/`. Use pexpect to drive devm or interactive shells — that mirrors production. Do NOT use `pexpect.spawn` to spawn `sbx exec -it bash` directly: pexpect allocates a fresh PTY for the child, which masks the real production shape where the user shell shares devm's PTY via inheritance. If you need a production-shape sbx exec, spawn a wrapper that does `os.execvp` into it (see `e2e/probes/probe-publish/main.go` for the pattern, or `e2e/test_sbx_anchor_07_wrapper_inherited_shell.py`).

Run a single test: `just e2e-one <pattern>`. Full suite: `just e2e` (serial by default at `-n 0` — parallel hits sbx-daemon contention flakes).

## Out-of-scope quirks worth knowing

- **DNS policy is host-global.** Two devm projects with overlapping `allowed_domains` interfere. Removing a domain from one project removes it for all.
- **Mounts are at the same path inside the VM** (mirrored). `mounts: [~/.aws:ro]` shows up at `/Users/<you>/.aws` inside the sandbox, not at `/mnt/...`.
- **Background daemons don't see service env** by default. They run during sbx startup before any `sbx exec -it` injects vars. If you need a daemon to see env, render it into the command directly.
