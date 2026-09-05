---
name: lifecycle
description: devm VM lifecycle commands — shell, start, reconcile, stop, teardown, status, validate. Use when you need to bring a VM up, apply config changes, or take it down.
---

# VM lifecycle command reference

## Command cheat-sheet

| Command | What it does |
|---|---|
| `devm shell [-- COMMAND]` | Attach a shell (or run COMMAND) inside a running, provisioned sandbox. Errors if the VM is stopped or not yet provisioned; run `devm start` first. |
| `devm start` | Cold-start (or adopt-in-place) the sandbox. Reads `devm.yaml` and drives every provisioning stage; the only command whose refusal on divergence carries "approve required." |
| `devm approve` | Review the changes to `devm.yaml` / `devm.me.yaml` since they were last approved and advance the snapshot. Interactive-only; no `--yes` flag ever. |
| `devm reconcile` | Diff the current config against the in-VM snapshot, apply live-bucket changes, and surface any pending teardown-bucket changes. |
| `devm stop` | Stop the VM (preserves disk). Use to free resources without losing installed state. |
| `devm teardown` | Destroy the VM and delete its disk image. Required after teardown-bucket changes. |
| `devm status` | Show VM state, active sessions, pending config diff, routing, DNS, CA trust, and proxy health. |
| `devm validate` | Lint `devm.yaml` (and `devm.me.yaml` if present) without touching the VM. |
| `devm pop mac <path>` | Open a Mac-native file with its default app; refuses paths that resolve into a devm-managed volume. Out-of-mirror paths get a live-sync session (see `devm status` for count). |
| `devm pop vm <path>` | Open a file from the project's guest workspace with its default app on the Mac. Out-of-mirror paths get a live-sync session (see `devm status` for count). |

`devm pop` — test/tuning overrides (read once at daemon start):

```
  DEVM_POP_SESSION_TTL_SECONDS       default 3600
  DEVM_POP_SESSION_GC_INTERVAL_SECONDS default 300
```

---

## `devm shell`

Warm-attach only — never starts, provisions, or adopts a VM. Queries the daemon for the VM's running state via `VMStatus`:

- **Not running** → prints `sandbox not running; run `devm start` first.` and exits non-zero. Never touches `StartVM`.
- **Running but `devm.target` not active** (never provisioned, or mid-provisioning) → prints `sandbox not yet provisioned; run `devm start` to finish provisioning.` and exits non-zero.
- **Running and provisioned** → attaches directly via `tart exec` (interactive, or runs `[-- COMMAND]` if given). The shell exits but the VM keeps running.

## `devm start`

The sole command that cold-starts or adopts the sandbox — and the sole command whose refusal on `devm.yaml` divergence carries "approve required" (see **The approve gate** below). Queries the daemon for the VM's running state via `VMStatus`, then does one of three things: **warm no-op** (already provisioned and running), **adopt-in-place** (running but never provisioned), or **cold start** (stopped/absent, or recovering from an interrupted provisioning run). Returns once `devm.target` is active — it never attaches a shell itself; run `devm shell` afterward for that.

### Warm no-op

If the VM is running (`Running=true`) and `devm.target` is active, the VM is already fully provisioned: `devm start` skips provisioning entirely and returns 0 immediately.

### Adopt-in-place

If the VM is running but `devm.target` is **not** active, the daemon never finished provisioning it — most commonly a bare `tart run` outside devm, or a daemon crash-restart before provisioning began. `devm start` checks for `/run/devm/provisioning` (written before the composed provisioning script starts, removed when it finishes successfully):

- **Absent** → the VM is pristine — running, but never provisioned, and (per the boot-integrity gate below) still inert and egress-locked. `devm start` adopts it in place: it runs the same provisioning tail as a cold start directly against the already-running VM, skipping `StartVM` and the exec-ready poll.
- **Present** → a previous provisioning run was interrupted (daemon crash, host sleep, killed exec) and left the guest in an unknown intermediate state. `devm start` never provisions onto a dirty slate: it stops and deletes the VM, then falls through to a fresh cold start.

### Cold start

If the VM is stopped or absent (or was just torn down as a dirty adopt-in-place above), `devm start`:

1. Resolves any `!secret` references from the on-disk secret store.
2. Sends a `StartVM` request to the daemon (which starts the VM and applies the network allow-list from `network.allow`).
3. Polls `tart exec <vmName> true` until exit 0, or up to 60 seconds.
4. Runs the provisioning tail described below (shared with adopt-in-place).
5. Returns 0 once `devm.target` is active. The VM keeps running; attach with `devm shell`, or use `devm stop` to stop it.

### The boot-integrity gate

The base image boots **locked and inert**. Egress is locked from the moment the VM comes up; `devm.target` — the unit that gates access to services and the shell — is installed but not enabled. Nothing user-facing starts on a bare boot. A VM the daemon didn't drive through provisioning (direct `tart run`, or a crash before provisioning began) therefore stays inert and locked: no ssh, no reachable services, no egress.

Provisioning is the daemon's job, not the guest's own boot sequence. It walks the guest and the Mac side through these stages, in this order:

| Stage | Side | When | What it does |
|---|---|---|---|
| `bundle` | guest | every run | Extract `/opt/devm` into the guest and run `install.sh` (devm CA install, PATH symlinks, mutagen-agent, systemd setup). Also flushes the base image's boot-time nftables lock — softnet is the egress boundary now. |
| `volume-sync` | Mac | every run, if `volumes:` or `repos:` is set | Establish a mutagen sync session for every volume and repo entity and wait for the initial sync to converge, so later stages see hydrated workspace + volume state. |
| `repo-clone` | guest | every run, per repo whose Mac-side mirror was empty | Clone the repo into the guest through iron-proxy. Repos with existing mirror content adopt in place instead. |
| `open` | guest | first boot, `startup:` non-empty, any service declares `templates:`, or a pending `packages:` diff | Egress opens fully for this window so `apt-get`, `curl … \| bash`, and friends work. |
| `packages` | guest | first boot (full list), or any later boot with a pending `packages:` diff | `apt-get update` + `apt-get install -y <packages>` on first boot; a targeted apt add/remove converge on a later boot. Both flow through the `apt_run` helper (per-file `Acquire::Retries=3` plus an outer three-attempt retry-with-backoff loop) so a transient mirror stall no longer tears the VM down. |
| `install` | guest | first boot only, if `install:` set | Run each `install:` command in order, open network. |
| `docker` | guest | first boot only, if `docker: true` | Install the Docker engine + runc shim; gate docker with everything else so it only starts after enforcement. |
| `templates` | guest | every boot, if any service declares `templates:` | Render every declared template file into its output path. |
| `startup` | guest | every boot, if `startup:` is non-empty | Run each `startup:` command, open network. |
| `commands` | guest | every run, per repo command flagged `startup: true` | Run each `repos.<name>.commands.<name>` marked `startup: true` in the repo's guest cwd, as the devm user via `with-devm-env`. Still under open egress. |
| `enforce` | Mac | every run | Flip softnet's egress policy authority back to restricted. No in-guest work — this is the Mac-side boundary that marks the classifier's teardown/debuggable split. A failure at or before this point is devm's own enforcement being broken, not a user service. |
| `services` | guest | every run | `daemon-reload` + `unmask ssh`; enable + start each declared service unit; health-poll each (bounded, tolerates `Type=oneshot`) until active/healthy or timeout — **before** `devm.target` starts. |
| _(finish)_ | guest | every run | `systemctl start devm.target` — brings up the gated services (ssh, docker, and your service units), all under enforcement. **Access is granted only now.** |

"Side" names where the stage marker is emitted from: `guest` stages come from the composed provisioning script; `Mac` stages come from the Mac-side orchestrator between guest scripts.

Any failing command aborts the whole provisioning run before `devm.target` starts, so a failure never grants access. `services` is the only stage that leaves the VM running for in-place debugging — a failure there is the user's service definition being broken *after* everything else worked. A failure at any earlier stage (from `bundle` through `enforce`) tears the VM down — `devm start` promises loud failure, never a half-created VM left behind. `templates` deliberately does not keep the VM even though it runs after `install:`/`docker:` (it runs under open egress, before `enforce` installs the real allowlist — a VM kept alive on a `templates` failure would be sitting there unenforced).

`install`/`docker` are gated by the `/var/lib/devm/provisioned` marker and only run once, on first boot; they're skipped on a later cold start (`devm stop` + `devm start` reuses the same disk, so installed tools and built artifacts are still there). `packages` runs its full list on first boot like the others, but also converges a pending `packages:` diff on a later cold start (a running VM instead converges the same diff live, via `devm reconcile`, under the project's current `network.allow` — see `packages` in the schema reference). `startup:` and `templates` run on every boot that opens the window. Restart-time workload otherwise comes back via systemd — enabled units auto-start when `devm.target` activates, and `devm stop` powers the guest off cleanly (`systemctl poweroff`) so docker containers with a restart policy are recorded as running-on-boot and come back up.

---

## `devm reconcile`

**VM stopped:** exits cleanly without applying anything, printing:

```
Sandbox stopped; config changes will apply on next `devm start`.
```

**VM running:** reads the in-VM snapshot (last-applied `schema.Config`), diffs it against the current config via `ComputeAllChanges`, and splits changes by bucket:

- **BucketLive changes** are passed to `ApplyLive` and reported as applied. Two kinds are actively wired today:
  - Per-service `env` add / remove / change — daemon pipes an updated bundle into the guest, rewriting `/etc/environment`.
  - `template` add / change / remove — re-runs the installer dispatcher script inside the VM via `tart exec`.
  
  All other BucketLive kinds (ports, path, service unit fields) have no apply path in `ApplyLive` and take effect at the next cold start, even though reconcile reports them as applied.

- **Package add / remove** is also BucketLive, but converges through a separate path, not `ApplyLive`: the daemon runs the apt diff inside the VM under the project's current `network.allow` — no allowlist widening, no restore, no teardown, no restart. `deb.debian.org` and `security.debian.org` (plus `download.docker.com` when `docker: true`) need to already be in `network.allow` for the diff to succeed.

- **BucketRestartVM changes** (e.g. `startup:` edits) are surfaced as pending under a distinct "restart" section, separate from recreate. On approval `devm reconcile` stops the VM (preserving its disk — no teardown); the user then runs `devm start` to cold-start and pick up the change. This is deterministic — the applying restart runs the freshly-composed provisioning script, so the change takes effect on that restart, not on some later boot.

- **BucketTeardownVM changes** are surfaced as pending under the "recreate" section. `devm reconcile` prompts the user; on approval it stops or tears down the VM automatically. The user then runs `devm start` to rebuild.

**Flags:** `--dry-run` (print diff, do not apply), `--yes` / `-y` (skip recreate confirmation), `--json`.

---

## `devm stop`

Prompts for confirmation (skip with `--yes` / `-y`), then sends `StopVM` to the daemon supervisor. The VM disk is preserved; installed packages and service state survive. The next `devm start` performs a cold start (which is fast because the disk and packages are already in place).

---

## `devm teardown`

Prompts for confirmation (skip with `--yes` / `-y`). Before stopping, removes this project's routes from the daemon (best-effort; silent if the daemon is down). Then sends `StopVM` to the daemon and calls `tart delete` to wipe the VM disk image. All installed state is lost; the next `devm start` performs a full cold start from scratch.

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

## The approve gate

The daemon persists a byte-level snapshot of the last-approved
`devm.yaml` (+ `devm.me.yaml` if present) per project under
`<RuntimeDir>/<projectID>/approved-snapshot/`. Gated commands
(`devm start`, `devm reconcile`) refuse when the current file bytes
differ from the snapshot; the error names two paths to approve:

1. Click the devm menu bar icon → Review (opens a diff window with
   Approve/Cancel buttons — see the devm Mac app, shipped separately).
2. Run `devm approve` in the terminal — an interactive-only verb that
   prints the diff and prompts y/N. **No `--yes` flag.** The human
   must be present at the terminal to answer; scripts cannot approve.

First-run bootstrap: a project with no prior snapshot has its current
`devm.yaml` written as the initial snapshot on the first `devm start`.
No prompt fires; the invariant kicks in as soon as there is history.

`devm status` shows the approve state as an informational line
("Approve gate: up to date" or "Approve gate: devm.yaml has changed
since last approval — Review"). Status never blocks.

---

## Bucket semantics

Every change kind is assigned to exactly one bucket in the `changeBucket` map (`internal/reconcile/diff.go`). The bucket determines what action is needed.

### BucketLive

`devm reconcile` handles these without stopping or destroying the VM. `network.allow` and secret value changes also fall here: the daemon regenerates iron-proxy's config and respawns it in place — no VM restart, no prompt.

Currently wired in `ApplyLive` (changes take effect immediately):

| Kind | Mechanism |
|---|---|
| Per-service env add / remove / change, top-level env add / remove / change, `path` change | Daemon pipes an updated bundle into the guest, rewriting `/etc/environment` (all three share the bundle-rebuild path) |
| Template add / change / remove | Runs installer dispatcher script in the VM via `tart exec` |

Also live, but converged outside `ApplyLive` (the reconcile handler applies it first, via a dedicated packages applier, before calling `ApplyLive`):

| Kind | Mechanism |
|---|---|
| `packages` add / remove | Top-level `packages:` list differs (set semantics — reordering is a no-op). Daemon runs the apt diff inside the VM under the project's current `network.allow` (`deb.debian.org`, `security.debian.org`, plus `download.docker.com` when `docker: true` need to be allowed already). A stopped VM converges the same diff on its next boot's open window instead. |

Classified BucketLive but no apply path in `ApplyLive` (take effect at next cold start):

| Kind | Note |
|---|---|
| Port add / remove / change | Softnet ingress reconciles port publishing off the merged snapshot elsewhere — no in-guest action needed for the port itself |
| Service `exec`, `restart`, `after`, `workdir`, `user`, `systemd` override, `hostname` | No apply code in `ApplyLive` |

### BucketTeardownVM

The VM must be fully deleted and recreated. `devm reconcile` surfaces these as pending and offers to tear down the VM automatically (requires confirmation). A subsequent `devm start` rebuilds from scratch.

| Kind | Trigger |
|---|---|
| `install` change | `install:` command list differs |
| Image change | `base_image:` field differs. Note: `BaseImage` is an empty struct with no fields; structural equality is always true, so `KindImageChange` cannot fire from a `devm.yaml` edit. |
| Identity change | `project:` identity fields differ |

### BucketRestartVM

VM stop + cold start — no teardown, no data loss. `devm reconcile` surfaces these under a "restart" section, distinct from recreate.

| Kind | Trigger |
|---|---|
| `startup` change | `startup:` command list differs. Deterministic: the daemon composes a fresh `startup.sh` and runs it inside the single provisioning script on the applying `devm stop` + `devm start` — the edit takes effect on that restart. |
