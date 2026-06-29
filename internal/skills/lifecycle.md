---
name: lifecycle
description: devm VM lifecycle commands — shell, reconcile, stop, teardown, status, validate. Use when you need to bring a VM up, apply config changes, or take it down.
---

# VM lifecycle command reference

## Command cheat-sheet

| Command | What it does |
|---|---|
| `devm shell` | Bring the VM up (cold-start if needed) and attach an interactive shell. Attaches immediately if already running. |
| `devm reconcile` | Re-render `.devm/`, diff against the in-VM snapshot, apply live-bucket changes, and surface any pending teardown-bucket changes. |
| `devm stop` | Stop the VM (preserves disk). Use to free resources without losing installed state. |
| `devm teardown` | Destroy the VM and delete its disk image. Required after teardown-bucket changes. |
| `devm status` | Show VM state, active sessions, pending config diff, routing, DNS, CA trust, and proxy health. |
| `devm validate` | Lint `devm.yaml` (and `devm.me.yaml` if present) without touching the VM. |

---

## `devm shell`

Acquires `.devm/lock`, re-renders `.devm/` from the current `devm.yaml`, then queries the daemon for the VM's running state.

### Warm attach

If the VM is already running (`Running=true` from the daemon), `devm shell` skips provisioning and attaches directly via `tart exec`. The shell exits but the VM keeps running.

### Cold start

If the VM is stopped or absent, `devm shell`:

1. Resolves any `!secret` references from the macOS login keychain.
2. Sends a `StartVM` request to the daemon (which starts the VM and applies the network allow-list from `network.allow`).
3. Polls `tart exec <vmName> true` until exit 0, or up to 60 seconds.
4. Runs `Provisioner.Run` in sequence:

   | # | Step name |
   |---|---|
   | 1 | `mkdir workspace parents` |
   | 2 | `install CA root` |
   | 3 | `write Caddyfile` |
   | 4 | `write dnsmasq config` |
   | 5 | `reload base services` |
   | 6 | `apt-get update` |
   | 7 | `apt-get install packages` |
   | 8 | `run install commands` |
   | 9 | `install service units` |
   | 10 | `systemctl daemon-reload` |
   | 11 | `enable + start services` |
   | 12 | `apply masks` |

5. Attaches an interactive shell via `tart exec`. The VM auto-stops when the shell exits.

The Provisioner steps are idempotent: re-running them on a stopped VM whose disk is already provisioned (after `devm stop`) is safe and fast (apt packages are already installed, units are already in place).

---

## `devm reconcile`

Always re-renders `.devm/` static files first (spec.yaml, Caddyfile, scripts — but not template installer scripts, which are the diff baseline). Then:

**VM stopped:** writes template installers too and exits cleanly, printing:

```
Sandbox stopped; config changes will apply on next `devm shell`.
```

**VM running:** reads the in-VM snapshot (last-applied `schema.Config`), diffs it against the current config via `ComputeAllChanges`, and splits changes by bucket:

- **BucketLive changes** are passed to `ApplyLive` and reported as applied. Two kinds are actively wired today:
  - Per-service `env` add / remove / change — rewrites `.devm/.env`; the workspace virtio-fs share surfaces the new file inside the VM immediately.
  - `template` add / change / remove — re-runs the installer dispatcher script inside the VM via `tart exec`.
  
  All other BucketLive kinds (ports, path, service unit fields) have no apply path in `ApplyLive` and take effect at the next cold start, even though reconcile reports them as applied.

- **BucketTeardownShell changes** are surfaced as pending. `devm reconcile` prompts the user; on approval it stops or tears down the VM automatically. The user then runs `devm shell` to rebuild.

**Two known gaps:**

1. **Network (`allow`) changes** — classified BucketLive in `changeBucket` but has no apply code in `ApplyLive`. The allow-list is passed to the daemon at `StartVM` time, so changes take effect at the next cold start, not on a running VM.

2. **Top-level `env:` changes** — `computeEnvChanges` iterates only per-service env (via `envOf`). A change made only at the top-level `env:` key produces no diff and takes effect at the next cold start via `installServiceUnits` (which merges top-level env into each service unit).

**Flags:** `--dry-run` (print diff, do not apply), `--yes` / `-y` (skip recreate confirmation), `--json`.

---

## `devm stop`

Prompts for confirmation (skip with `--yes` / `-y`), then sends `StopVM` to the daemon supervisor. The VM disk is preserved; installed packages and service state survive. The next `devm shell` performs a cold start (which is fast because the disk and packages are already in place).

---

## `devm teardown`

Prompts for confirmation (skip with `--yes` / `-y`). Before stopping, removes this project's routes from the daemon (best-effort; silent if the daemon is down). Then sends `StopVM` to the daemon and calls `tart delete` to wipe the VM disk image. All installed state is lost. `.devm/` is not touched; the next `devm shell` performs a full cold start from scratch.

Required after any **teardown-bucket** change (see Bucket semantics below).

---

## `devm status`

Reports (text or `--json`):

| Field | What it shows |
|---|---|
| Sandbox name | VM name from `project.vm_name` |
| State | `absent` / `stopped` / `running` |
| Active sessions | TTY, command, PID, owner (running VMs only; probed via `tart exec`) |
| Pending changes | Count of live-bucket and recreate-bucket pending changes vs. the in-VM snapshot (running VMs only) |
| Routing | Proxy mode, per-hostname route table, proxy reachability |
| DNS health | Whether the system resolver can reach the daemon's DNS for `*.test` names |
| CA trust | Whether devm's local CA root is installed in the System Keychain |
| Proxy health | Whether something is listening on `:443` (500ms TCP dial) |

---

## `devm validate`

Calls `config.Load` without touching the VM. Validates `devm.yaml` and `devm.me.yaml` (if present) against the schema. On success, prints `OK — N service(s) configured` and exits 0.

<!-- migration-note-start -->
`config.Load` runs `CheckLegacyKeys` before the typed parse. Configs using removed keys get a migration-pointer error rather than a silent parse failure. For example, `network.allowed_domains:` must be renamed to `network.allow:`, `project.sandbox_name:` must be renamed to `project.vm_name:`, and `project.hostname_apex:` is no longer supported.
<!-- migration-note-end -->

---

## Bucket semantics

Every change kind is assigned to exactly one bucket in the `changeBucket` map (`internal/orchestrator/diff.go`). The bucket determines what action is needed.

### BucketLive

`devm reconcile` handles these without stopping or destroying the VM.

Currently wired in `ApplyLive` (changes take effect immediately):

| Kind | Mechanism |
|---|---|
| Per-service env add / remove / change | Rewrites `.devm/.env`; workspace mount surfaces it in the VM |
| Template add / change / remove | Runs installer dispatcher script in the VM via `tart exec` |

Classified BucketLive but no apply path in `ApplyLive` (take effect at next cold start):

| Kind | Note |
|---|---|
| Port add / remove / change | No apply code in `ApplyLive` |
| Network `allow` add / remove | No apply code in `ApplyLive` — the allow-list is applied at `StartVM` time, so changes land on the next cold start |
| `path` change | No apply code in `ApplyLive` |
| Service `exec`, `restart`, `after`, `workdir`, `user`, `systemd` override, `hostname` | No apply code in `ApplyLive` |

### BucketTeardownShell

The VM must be fully deleted and recreated. `devm reconcile` surfaces these as pending and offers to tear down the VM automatically (requires confirmation). A subsequent `devm shell` rebuilds from scratch.

| Kind | Trigger |
|---|---|
| `install` change | `install:` command list differs |
| `packages` change | `packages:` list differs |
| Mount add / remove | `mounts:` list differs |
| Mask add / remove | Per-service `masks:` list differs. mask `path` must be relative to the repo root (absolute paths, `~/`, and `$VAR` are rejected at `devm validate`). |
| Image change | `base_image:` field differs. Note: `BaseImage` is an empty struct with no fields; structural equality is always true, so `KindImageChange` cannot fire from a `devm.yaml` edit. |
| Identity change | `project:` identity fields differ |

### BucketStopShell

Reserved. No change kind maps to this bucket today.
