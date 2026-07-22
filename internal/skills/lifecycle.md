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

Queries the daemon for the VM's running state via `VMStatus`, then does one of three things: **warm attach** (already provisioned and running), **adopt-in-place** (running but never provisioned), or **cold start** (stopped/absent, or recovering from an interrupted provisioning run).

### Warm attach

If the VM is running (`Running=true`), `devm shell` checks whether `devm.target` is active — the gate unit that provisioning starts last, once all services are healthy under enforced egress. If it's active, the VM is fully provisioned: `devm shell` skips provisioning and attaches directly. The shell exits but the VM keeps running.

### Adopt-in-place

If the VM is running but `devm.target` is **not** active, the daemon never finished provisioning it — most commonly a bare `tart run` outside devm, or a daemon crash-restart before provisioning began. `devm shell` checks for `/run/devm/provisioning` (written before the composed provisioning script starts, removed when it finishes successfully):

- **Absent** → the VM is pristine — running, but never provisioned, and (per the boot-integrity gate below) still inert and egress-locked. `devm shell` adopts it in place: it runs the same provisioning tail as a cold start directly against the already-running VM, skipping `StartVM` and the exec-ready poll.
- **Present** → a previous provisioning run was interrupted (daemon crash, host sleep, killed exec) and left the guest in an unknown intermediate state. `devm shell` never provisions onto a dirty slate: it stops and deletes the VM, then falls through to a fresh cold start.

### Cold start

If the VM is stopped or absent (or was just torn down as a dirty adopt-in-place above), `devm shell`:

1. Resolves any `!secret` references from the macOS login keychain.
2. Sends a `StartVM` request to the daemon (which starts the VM and applies the network allow-list from `network.allow`).
3. Polls `tart exec <vmName> true` until exit 0, or up to 60 seconds.
4. Runs the provisioning tail described below (shared with adopt-in-place).
5. Attaches an interactive shell via `tart exec`. The shell exits but the VM keeps running; use `devm stop` to stop it.

### The boot-integrity gate

The base image boots **locked and inert**. Egress is locked from the moment the VM comes up; `devm.target` — the unit that gates access to services and the shell — is installed but not enabled. Nothing user-facing starts on a bare boot. A VM the daemon didn't drive through provisioning (direct `tart run`, or a crash before provisioning began) therefore stays inert and locked: no ssh, no reachable services, no egress.

Provisioning is the daemon's job, not the guest's own boot sequence. It walks the guest through these stages:

| Stage | When | What it does |
|---|---|---|
| _(preamble)_ | every run | Set up devm's in-guest state and the CA trust. |
| `open` | first boot, or `startup:` non-empty, or any service declares `templates:` | Egress opens fully for this window so `apt-get`, `curl … \| bash`, and friends work. |
| `packages` | first boot only, if `packages:` set | `apt-get update` + `apt-get install -y <packages>`. |
| `install` | first boot only, if `install:` set | Run each `install:` command in order, open network. |
| `docker` | first boot only, if `docker: true` | Install the Docker engine + runc shim; gate docker with everything else so it only starts after enforcement. |
| `templates` | every boot, if any service declares `templates:` | Render every declared template file into its output path. |
| `startup` | every boot, if `startup:` is non-empty | Run each `startup:` command, open network. |
| `enforce` | every boot | Stage boundary marking the classifier's teardown/debuggable split — a failure at or before this point is devm's own enforcement being broken, not a user service. Does no in-guest work — the egress policy flip happens on the Mac side. |
| `services` | every boot | Apply per-service mask overlays; enable + start each declared service unit; health-poll each until active/healthy or timeout — **before** `devm.target` starts. |
| _(finish)_ | every boot | `systemctl start devm.target` — brings up the gated services (ssh, in-VM reverse-proxy, docker, and your service units), all under enforcement. **Access is granted only now.** |

Any failing command aborts the whole provisioning run before `devm.target` starts, so a failure never grants access. A failure at the `templates` or `services` stage leaves the VM running for in-place debugging (the user's service/template definition is what's broken); any earlier-stage failure (`open` through `enforce`) tears the VM down — `devm shell` promises loud failure, never a half-created VM left behind.

`packages`/`install`/`docker` are gated by the `/var/lib/devm/provisioned` marker and only run once, on first boot; they're skipped on a later cold start (`devm stop` + `devm shell` reuses the same disk, so installed tools and built artifacts are still there). `startup:` and `templates` run on every boot that opens the window. Restart-time workload otherwise comes back via systemd — enabled units auto-start when `devm.target` activates, and `devm stop` powers the guest off cleanly (`systemctl poweroff`) so docker containers with a restart policy are recorded as running-on-boot and come back up.

---

## `devm reconcile`

Always re-renders `.devm/` static files first (spec.yaml, Caddyfile, scripts — but not template installer scripts, which are the diff baseline). Then:

**VM stopped:** writes template installers too and exits cleanly, printing:

```
Sandbox stopped; config changes will apply on next `devm shell`.
```

**VM running:** reads the in-VM snapshot (last-applied `schema.Config`), diffs it against the current config via `ComputeAllChanges`, and splits changes by bucket:

- **BucketLive changes** are passed to `ApplyLive` and reported as applied. Two kinds are actively wired today:
  - Per-service `env` add / remove / change — daemon pipes an updated bundle into the guest at `/opt/devm/.env`.
  - `template` add / change / remove — re-runs the installer dispatcher script inside the VM via `tart exec`.
  
  All other BucketLive kinds (ports, path, service unit fields) have no apply path in `ApplyLive` and take effect at the next cold start, even though reconcile reports them as applied.

- **BucketRestartVM changes** (e.g. `startup:` edits) are surfaced as pending under a distinct "restart" section, separate from recreate. On approval `devm reconcile` stops the VM (preserving its disk — no teardown); the user then runs `devm shell` to cold-start and pick up the change. This is deterministic — the applying restart runs the freshly-composed provisioning script, so the change takes effect on that restart, not on some later boot.

- **BucketTeardownVM changes** are surfaced as pending under the "recreate" section. `devm reconcile` prompts the user; on approval it stops or tears down the VM automatically. The user then runs `devm shell` to rebuild.

**Two known gaps:**

1. **Network (`allow`) changes** — classified BucketLive in `changeBucket` but has no apply code in `ApplyLive`. The allow-list is passed to the daemon at `StartVM` time, so changes take effect at the next cold start, not on a running VM.

2. **Top-level `env:` changes** — `computeEnvChanges` iterates only per-service env (via `envOf`). A change made only at the top-level `env:` key produces no diff and takes effect at the next cold start. The devm bundle builder (`internal/devmbundle/bundle.go`, `Build` function) merges top-level env into each service's env before rendering systemd units, so top-level entries like `env: { GITHUB_TOKEN: !secret ... }` reach the per-service systemd units' `Environment=` lines.

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
| Sandbox name | VM name from `project.name` |
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
`config.Load` runs `CheckUnknownKeys` before the typed parse. Any key that isn't part of the current schema — a typo, or a field removed in a newer devm — hard-fails with an `unknown field "<key>" at <scope>` error listing the valid keys, rather than being silently dropped. There is no per-key migration pointer; removed keys (e.g. `project.id`, `project.vm_name`, `network.allowed_domains`, `project.hostname_apex`) simply surface as unknown fields.
<!-- migration-note-end -->

---

## Bucket semantics

Every change kind is assigned to exactly one bucket in the `changeBucket` map (`internal/reconcile/diff.go`). The bucket determines what action is needed.

### BucketLive

`devm reconcile` handles these without stopping or destroying the VM.

Currently wired in `ApplyLive` (changes take effect immediately):

| Kind | Mechanism |
|---|---|
| Per-service env add / remove / change | Daemon pipes an updated bundle into the guest at `/opt/devm/.env` |
| Template add / change / remove | Runs installer dispatcher script in the VM via `tart exec` |

Classified BucketLive but no apply path in `ApplyLive` (take effect at next cold start):

| Kind | Note |
|---|---|
| Port add / remove / change | No apply code in `ApplyLive` |
| Network `allow` add / remove | No apply code in `ApplyLive` — the allow-list is applied at `StartVM` time, so changes land on the next cold start |
| `path` change | No apply code in `ApplyLive` |
| Service `exec`, `restart`, `after`, `workdir`, `user`, `systemd` override, `hostname` | No apply code in `ApplyLive` |

### BucketTeardownVM

The VM must be fully deleted and recreated. `devm reconcile` surfaces these as pending and offers to tear down the VM automatically (requires confirmation). A subsequent `devm shell` rebuilds from scratch.

| Kind | Trigger |
|---|---|
| `install` change | `install:` command list differs |
| `packages` change | `packages:` list differs |
| Mount add / remove | `mounts:` list differs |
| Mask add / remove | Per-service `masks:` list differs. mask `path` must be relative to the repo root (absolute paths, `~`, and `$VAR` are rejected at `devm validate`). |
| Image change | `base_image:` field differs. Note: `BaseImage` is an empty struct with no fields; structural equality is always true, so `KindImageChange` cannot fire from a `devm.yaml` edit. |
| Identity change | `project:` identity fields differ |

### BucketRestartVM

VM stop + cold start — no teardown, no data loss. `devm reconcile` surfaces these under a "restart" section, distinct from recreate.

| Kind | Trigger |
|---|---|
| `startup` change | `startup:` command list differs. Deterministic: the daemon composes a fresh `startup.sh` and runs it inside the single provisioning script on the applying `devm stop` + `devm shell` — the edit takes effect on that restart. |
