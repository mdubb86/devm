---
name: schema
description: devm.yaml schema reference — every top-level field, type, and bucket semantics.
---

# devm.yaml schema reference

## Top-level fields

| Field | Type | Bucket | Purpose |
|---|---|---|---|
| `project` | object | recreate | Project identity (required). |
| `base_image` | object | recreate | Accepted for YAML compatibility; has no active fields. |
| `docker` | object | varies | Installs Docker in the VM and makes container egress trust iron-proxy transparently. `enabled` is a required bool when the block is present; `buildCache` optionally sizes the BuildKit cache volume as a `"6G"`-style absolute size or a `"15%"`-style fraction of disk. Omit the block entirely to disable docker. `docker.enabled` is **recreate**; `docker.buildCache` is **restart**. |
| `network` | object | live | Iron-proxy outbound allowlist. |
| `env` | map[string]EnvValue | live | Project-wide environment variables forwarded to all services. |
| `services` | map[string]Service | varies | Named service definitions; bucket depends on which sub-field changes (see Services section). |
| `packages` | []string | live | Apt packages. A running VM converges under the project's current `network.allow` — the Debian mirrors need to be reachable; a stopped VM converges during the next boot's provisioning window. |
| `volumes` | map[string]Volume | live | Per-project named persistent stores. Key = volume name; value is either a bare guest path string or a `{path, label, ignore}` mapping. Data lives on the Mac side under `~/Library/Application Support/devm/<projectID>/<label>/` and survives `devm teardown`. See the `volumes` section below. |
| `repos` | map[string]RepoConfig | varies | Declares the project's git repos to hydrate via `git clone` at cold-start, keyed by an arbitrary schema id. Exactly one entry is the primary workspace repo. `url`/`secret` are **restart-VM** (iron-proxy clones at boot using these); every other field is **live** (mutagen-session-only). Optional — omit for utility VMs with no repo. See the `repos` section below. |
| `path` | []string | live | Directories prepended to `$PATH` inside the VM. |
| `disk` | string | recreate | Override the guest's virtual disk size in GB (e.g. `"64GB"`). Defaults to 32 (baked into devm-base). tart's disk resize is grow-only, so values below 32 GB are rejected. |
| `memory` | string | restart | Override the VM's RAM, e.g. `"8G"`, `"16G"`. Unset uses the base image default. Requires a G/GB suffix; the magnitude must be a positive integer. Removing this field from devm.yaml does not revert the running VM's tart config; the previously-set value persists across reconcile-restarts. Use `devm teardown` to fully reset to the base image default. Not overridable in `devm.me.yaml`. |
| `cpu` | int | restart | Override the VM's virtual CPU count. Unset uses the base image default. Must be a positive integer. Removing this field from devm.yaml does not revert the running VM's tart config; the previously-set value persists across reconcile-restarts. Use `devm teardown` to fully reset to the base image default. Not overridable in `devm.me.yaml`. |
| `swap` | string | restart | Override the guest's swap size as an absolute (`"16G"`) or a percent of RAM (`"30%"`). Unset defaults to 50% of RAM. Validated at load time via the shared size/percent parser. |
| `snapshots` | map[string]Snapshot | restart | Declared hook-driven captures of in-guest data to compressed Mac-side archives. Each entry pairs with `backup_<name>` / `restore_<name>` functions in `devm.sh`. Adding, removing, or changing an entry requires `devm stop && devm start`. See the `snapshots` section below. |
| `cron` | map[string]string | live | Scheduled runs of `devm.sh` functions: key is the function name, value is a five-field cron expression. Entries merge with `devm.me.yaml` per key. Changes apply on `devm reconcile`. See the `cron` section below. |
| `guest` | object | live | Gates the requests `gdevm` (the guest-side dispatcher) may send to the daemon. Sub-fields `propose` and `passthrough` are booleans defaulting to `true`; set to `false` to refuse the corresponding request class with 403. Project-wide by design — not overridable in `devm.me.yaml`. |
| `mac_services` | []{name, port} | live | Mac loopback ports this VM may reach. Guest URL: `<name>.host.test:<port>`. Each `name` is a DNS label (used in the URL only); `port` is the authorization. Reserved ports 53/80/123/443 are refused at load. `host.test` is a reserved zone — no route hostname may fall under it. Project-wide trust; not overridable in `devm.me.yaml`. |
| `peer_services` | []{project} | live | Cooperating devm projects this VM may reach by their native hostnames (`*.<project>.test`). Grants reach to every hostname the peer project declares. The peer's own TLS cert is presented (both VMs install the devm CA). Unknown or self peers are refused at load. Project-wide trust; not overridable in `devm.me.yaml`. |
| `ssh` | []string | recreate | Mac ssh config aliases the guest may reach through a per-project SSH jump. Each alias is resolved via `ssh -G` on the Mac at apply time; `ProxyJump` chains are rejected (no multi-hop in v1). The guest's ssh client config ships with one `Host <alias>` block per entry and `PreferredAuthentications publickey` on every target. |

---

## `guest`

Two booleans gating what the in-VM `gdevm` binary may ask the daemon to do:

- **`propose`**: `gdevm propose` records a devm.yaml / devm.sh (etc.) edit signal on the Mac side. Both fields default `true`. Set to `false` and the daemon refuses guest-source propose calls with 403; Mac-side `devm propose` is not affected.
- **`passthrough`**: `gdevm passthrough <duration> --reason "..."` submits a request for a passthrough egress window awaiting Mac-side approval. Set to `false` and the daemon refuses with 403. Mac-side `devm passthrough open` is not affected — the host is always in control of its own egress.

Example:

```yaml
guest:
  propose: false        # human curates devm.yaml directly; guest cannot signal edits
  passthrough: true     # guest may still ask for open-egress windows
```

---

## `project`

Required. Identifies the project.

| Field | Type | Required | Purpose |
|---|---|---|---|
| `name` | string | yes | Project name. Serves as both the devm-owned identity namespace (secrets, routes, state, iron-proxy, ssh keys) and the literal Tart VM instance name. Must contain no whitespace, `/`, `\`, or `..`. |

Validation: `name` is required.

Changing any `project` field is in the **recreate** bucket — the VM must be deleted and recreated from scratch.

---

## `network`

Controls outbound access enforced by iron-proxy (bucket: **live**).

| Field | Type | Purpose |
|---|---|---|
| `allow` | []AllowEntry | Hostnames the VM is permitted to reach, matched by SNI for TLS connections or HTTP Host header for plain HTTP. Each entry is a bare host scalar or a `{host, secrets}` mapping. |

On a running VM, `devm reconcile` applies `allow` changes automatically by regenerating iron-proxy's config and respawning it — no VM restart, no prompt. On a stopped VM, changes take effect at the next `devm start` cold start.

Each allow entry accepts two forms:

- **Bare scalar** — just the hostname string: `- api.example.com`
- **Mapping** — `{host, secrets}`: names a host and lists which `!secret` values iron-proxy may inject on requests to that host only. Secrets not named in any allow entry are omitted from iron-proxy config and never injected.

Bare `*` is the wildcard sentinel: it matches any destination host, permitting unrestricted outbound access through iron-proxy.

A host may carry a path pattern: everything from the first `/` scopes reachability to matching request paths. A pattern ending `/*` matches the whole subtree; anything else must match exactly. Query strings never participate in matching — drop them. Secrets on a path-scoped entry still inject host-wide.

```yaml
network:
  allow:
    - api.example.com                        # bare scalar — whole host
    - cdn.example.com/dl/v2/*                # only this path subtree
    - host: api.other.com
      secrets: [my_api_key]                  # inject my_api_key only to this host
    - "*"                                    # wildcard — any host
```

---

## `env`

`map[string]EnvValue` — bucket: **live**.

Project-wide environment variables injected into all services. Values are literal strings or `!secret` references resolved from the on-disk secret store:

```yaml
env:
  RAILS_ENV: development
  API_KEY: !secret my-api-key
```

Reserved keys (devm-injected; cannot be overridden): `WORKSPACE`, `DEVM_SIDE` (= `guest` inside the VM, = `host` under `devm run`).

Substitution rules in values:
- `$WORKSPACE` (or `${WORKSPACE}`) expands to the primary repo's GUEST path (`/home/devm/<primary-label>`), or `/home/devm` when no repos are declared. Substitution happens at load time on the Mac; the expanded value lands in `/etc/environment` inside the guest, so `$WORKSPACE` in scripts resolves the same way at guest-shell run time.
- `$$` → literal `$`.
- Any other `$VAR` reference is an error.

Per-service `env` entries win over top-level `env` on key collision.

Note: `devm reconcile` detects env changes via per-service `env` entries only. A change to top-level `env` with no corresponding per-service change produces no diff output; it takes effect on the next `devm start` cold start.

---

## `path`

`[]string` — bucket: **live**.

Directories prepended to `$PATH` inside the VM. Changes take effect in new interactive shells and newly-started services; running processes keep their current `$PATH`.

Final `$PATH` shape inside the VM:

```
<path[0]>:<path[1]>:...:/opt/devm/scripts:$PATH
```

Rules:
- Entries must be absolute (start with `/` or `$WORKSPACE`).
- `$WORKSPACE` expands to the primary repo's guest path (`/home/devm/<primary-label>`, or `/home/devm` when no repos are declared). See the `env` section for the full substitution rules. `$$` → literal `$`.
- Empty entries and `~` expansion are rejected.

---

## `devm.sh`

Not a `devm.yaml` field — a sibling file at the project root. `devm.sh` is
a bash function library: every top-level `name() { ... }` (or `function
name { ... }`) declaration becomes a callable function, named
`[a-z][a-z0-9-]*` (kebab-case, starting with a letter).

Two function names are magic, called automatically by the provisioner:

- **`install()`** — runs once, on first boot only (gated by
  `/var/lib/devm/provisioned`, same as before). Use for one-time setup:
  `sudo apt-get install -y <pkg>`, toolchain downloads, anything that
  shouldn't repeat on every boot.
- **`startup()`** — runs on every boot that opens the provisioning
  window (first boot, or any service declares `templates:`), before the
  allowlist restricts egress. Use for per-boot setup that needs
  unrestricted network (fetch/refresh something, warm a cache).

Both run as the guest `devm` user under `passthrough` authority mode
(iron-proxy in the path, MITM'd and audited, but not allowlist-gated
yet), with NOPASSWD sudo available. A failing `install()` or `startup()`
aborts provisioning: `devm.target` never starts, no access is granted,
and the VM is torn down.

Every other function name is only called where something references it:

- `repos.<name>.commands` — function names runnable against that repo
  via `gdevm run <name>` from the guest shell.
- `services.<name>.exec` (string form) — the function backing that
  service's process; devm renders a wrapper unit that invokes it.
- `cron.<name>` — the function that runs on that schedule.
- from inside `devm.sh` itself — functions can call each other like any
  bash script.

`devm.me.sh` is a per-machine override: same shape, sourced after
`devm.sh` so its definitions win on name collision (last-source-wins),
same pairing as `devm.me.yaml`.

---

## `packages`

`[]string` — bucket: **live**.

Apt package names installed via `apt-get install -y`. Adding or removing entries converges without a teardown: on a running VM, the daemon runs the apt diff under the project's current `network.allow` — no allowlist widening or restoring. Add `deb.debian.org` and `security.debian.org` (plus `download.docker.com` when `docker.enabled: true`) to `network.allow`, or open a `devm passthrough open` window and re-run, if the diff 403s (`devm denials` shows the blocked mirrors). On a stopped VM, the same diff converges during the next boot's provisioning window, which runs under open egress regardless. Reordering the list is a no-op — only membership changes trigger a converge.

```yaml
packages:
  - postgresql-client
  - redis-tools
```

Note: if `packages` is empty, `apt-get update` is skipped entirely during provisioning.

---

## `services`

`map[string]Service` — bucket varies by sub-field.

Named service definitions. Each key is the service name and its systemd unit name; names starting with `devm-` or `gdevm-` are reserved and refused at load.

| Field | Type | Bucket | Purpose |
|---|---|---|---|
| `port` | int or "IP:PORT" | live | VM-side listen port. String form (`"0.0.0.0:8080"`) is parsed for a bind IP, but that IP is silently ignored: every service's host-side listener binds on the project's allocated `127.42.0.N` address (its own loopback IP, one per project). For LAN reachability of a service's hostname, use `expose_host: true` (below) — that opts the hostname into devm's shared LAN dispatcher on `0.0.0.0:42000`, which is the correct mechanism. |
| `hostname` | string | live | Hostname this service answers to, dispatched by the daemon's ProxyServer (from the Mac) and its guest-origin listener (from inside the VM). Must end in `.test`. May be a single-label TLS-style wildcard (`*.pieces.test`), matching exactly one consumed label; cross-project overlaps are rejected at `route apply`. See routing skill for details. |
| `direct` | bool | live | Treat this service as raw TCP end-to-end instead of HTTP-fronted: skips the daemon's ProxyServer hostname dispatch and its guest-origin listener. DNS still answers `hostname` with the project's `127.42.0.N` on the Mac (and `127.0.0.1` inside the VM), and the client connects directly — every service with `hostname` + `port` is exposed on softnet regardless of `direct`; this flag only controls how it's fronted. Use for non-HTTP protocols (Postgres, gRPC) a reverse proxy can't front. Requires `hostname`. Default `false`. |
| `expose_host` | bool | live | Opt this service's `hostname` into devm's shared LAN dispatcher (host `0.0.0.0:42000`), so other devices on the LAN can reach it by hostname. Requires `hostname`. Independent of `direct`. Default `false`. |
| `env` | map[string]EnvValue | live | Per-service environment variables (same `!secret` syntax as top-level `env`). |
| `templates` | []Template | live | Files rendered from source scripts and written into the VM. Each has `source` (project-relative path), `output` (absolute path in VM), and optional `sudo` (default `false`; set `true` when `output` is under a root-owned dir like `/etc` so the installer escalates and the resulting file lands root-owned). |
| `exec` | string or []string | live | The service's process. A single string names a `devm.sh`/`devm.me.sh` function to run (devm renders a wrapper unit that invokes it); a list is a literal argv (command + arguments) run directly. |
| `workdir` | string | live | Working directory for the service process. |
| `restart` | string | live | Restart policy: `no`, `on-failure`, or `always`. |
| `after` | []string | live | Service names this service waits for at start (ordering only). |
| `user` | string | live | Unix user to run the service as. |
| `systemd` | string | live | Name of an existing systemd unit to manage. Mutually exclusive with `exec`, `restart`, `after`, `workdir`, and `user`. |

Validation rules:
- A service must define at least one of `port`, `exec`, or `systemd`.
- `hostname` must end in `.test`.
- `direct: true` requires a `hostname`.
- `expose_host: true` requires a `hostname`.
- Port values must be in range 1–65535; no two services may share a port or a hostname.
- Template `source` must be project-relative (no `../` traversal); `output` must be absolute.
- Template `sudo` defaults to `false` (installer runs as the guest user, file lands owned by that user). Set `true` for outputs under `/etc`, `/usr`, `/var` where the guest user cannot write; the installer then uses `sudo install -o root -g root` and the file lands root-owned.

---

## `volumes`

Per-project named persistent stores. State that outlives `devm teardown` — Postgres data, browser caches, credential caches, anything a user rebuilds by hand today because it dies with the VM.

```yaml
volumes:
  postgres-data: /var/lib/postgresql/data
  claude-cache: /home/devm/.cache/claude
  design-tokens:
    path: /home/devm/design-tokens
    label: tokens
    ignore:
      - "*.log"
```

- **Name** (the map key): must match `[a-z0-9][a-z0-9._-]*`. Scoped to the project — different projects can reuse the same name without collision.
- **Value**: either a bare guest path string, or a mapping with `path`, `label`, and `ignore`.
  - **`path`**: required; absolute; no `..` traversal; can't overlap the workspace mount root.
  - **`label`**: optional. Names the mutagen sync session for this volume. Defaults to the leaf directory name of `path` (e.g. `/home/devm/.cache/claude` → `claude`). Must be unique across every `repos` and `volumes` entry in the config — see label collision under the `repos` section below.
  - **`ignore`**: optional list of mutagen sync ignore patterns.

Storage lives Mac-side at `~/Library/Application Support/devm/<projectID>/<label>/`. Delivery is a mutagen two-way sync session between that Mac dir and the guest path, so `devm teardown` (which wipes the VM disk) leaves volume data alone. A subsequent cold-start resumes syncing the same Mac dir against the declared guest path.

Add / remove / retarget a volume ⇒ **live** bucket: mutagen sessions start/stop without a VM cycle.

On first sync with existing content on one or both sides, devm's in-sync guard compares entry count, total size, and a hash of each side's sorted top-100 paths before handing anything to mutagen. Two empty sides, or two sides that already agree, proceed straight to sync. Sides that disagree are rejected rather than guessed at — clear the stale side and retry.

Discovery: `devm volume ls` lists the current project's volumes with name, guest path, Mac path, and size. Deletion is manual (`rm -rf` at the Mac path), or `devm purge` for orphan cleanup of projects that no longer exist.

---

## `repos`

`map[string]RepoConfig` — bucket: **restart** (add/remove/retarget currently requires a VM stop + cold start; becomes **live** for add/remove once mutagen session hot-add lands).

Declares the project's git repos, keyed by an arbitrary schema id (the map key). Devm `git clone`s each one via `tart exec` at cold-start. Optional — omit entirely for a utility VM that runs only tools, with no repo checkout.

```yaml
repos:
  main:
    secret: gh_token
    primary: true
  data:
    url: https://github.com/me/data.git
    secret: gh_token
    label: data
    ignore:
      - "*.log"
```

| Field | Type | Required | Purpose |
|---|---|---|---|
| `url` | string | only for non-primary entries | Git clone URL. Omit only on the primary entry — its URL derives from `git remote get-url origin` in the Mac-side project directory. |
| `secret` | string | yes | Names a devm secret-store entry; iron-proxy substitutes it into the clone's `Authorization` header. |
| `label` | string | no | Names the mutagen sync session for this repo. Defaults to the bare clone name (`git@github.com:me/foo.git` → `foo`) when `url` is set, or to the Mac-side project directory's basename for the URL-omitted primary. |
| `volume` | bool | no | When true, backs this repo with a devm-managed volume instead of a plain bind mount. Defaults `true` for the primary, `false` for secondaries. The primary cannot set `volume: false`. |
| `primary` | bool | no | Marks this entry as the project's primary workspace repo. |
| `ignore` | []string | no | Mutagen sync ignore patterns. |
| `commands` | []string | no | Function names (from `devm.sh`/`devm.me.sh`) runnable against this repo via `gdevm run <name>`. |

##### `commands:` (optional)

A list of function names, defined in the project's `devm.sh` (or `devm.me.sh`), runnable against this repo via `gdevm run <name>` from the guest. Names must match `/^[a-z][a-z0-9-]*$/`. Uniqueness is per-repo; two repos can both list a `test` function — the guest-side `gdevm run <name>` dispatcher picks the right one from `$PWD`.

Example:

```yaml
repos:
  main:
    secret: gh_token
    commands:
      - install
      - test
      - lint
```

**Primary determination** — exactly one of these must hold across `repos`:
- one entry sets `primary: true` explicitly, or
- exactly one entry omits `url:` (that omission implies it's primary, deriving its URL from `git remote get-url origin`).

Zero or multiple explicit `primary: true` entries, or zero or multiple `url`-omitted entries, is a validation error at load time. Non-primary entries must always declare `url:` explicitly.

**Validation**:
- **Label collisions**: every `repos` and `volumes` entry resolves to a label (explicit `label:`, or its derived default). Two entries — repo-repo, repo-volume, or volume-volume — resolving to the same label is rejected; set an explicit `label:` on one to disambiguate.
- **Reserved project names**: `project.name` may not collide with a devm-internal storage directory name (`bin`, `state`, `iron-proxy`, `mutagen`, `volumes`) — repo and volume storage lives under the project's own directory, and a name collision would shadow devm's internals.

---

## `snapshots`

`map[string]Snapshot` — bucket: **restart**.

Declared captures of in-guest data to compressed Mac-side archives. Each entry runs in one of two mutually exclusive modes:

- **Path mode** — set `path:` to a guest-side filesystem path and devm captures the files under it directly. Optional `pre_backup_<name>` / `post_backup_<name>` / `pre_restore_<name>` / `post_restore_<name>` hooks in `devm.sh` wrap the copy.
- **Stream mode** — leave `path:` unset and devm pipes `backup_<name>`'s stdout into the archive and feeds `restore_<name>`'s stdin on the way back. Both hooks are required.

Having `path:` set AND a bare `backup_<name>` / `restore_<name>` function declared is ambiguous and rejected at load.

| Field | Type | Required | Purpose |
|---|---|---|---|
| `path` | string | no | Guest-side filesystem path for path-mode capture. Unset ⇒ stream mode. |
| `cron` | string | no | Five-field cron expression (`"0 3 * * *"`) scheduling periodic captures. Requires `keep`. |
| `keep` | int | with `cron` | Retention count; older archives beyond this count are pruned after a successful capture. Must be `> 0`. |
| `on_stop` | bool | no | Capture before `devm stop`. Default `false`. |
| `on_teardown` | bool | no | Capture before `devm teardown`. Default `false`. |
| `timeout` | map | no | Per-phase deadlines: `{ pre, step, post }`. See below. |

`timeout` sub-fields (all optional Go duration strings, e.g. `"30s"`, `"10m"`):

| Field | Default | Purpose |
|---|---|---|
| `pre` | `30s` | Deadline for the pre-hook (`pre_backup_<name>` / `pre_restore_<name>`). |
| `step` | `10m` | Deadline for the main capture / restore step (the path copy, or `backup_<name>` / `restore_<name>`). |
| `post` | `30s` | Deadline for the post-hook (`post_backup_<name>` / `post_restore_<name>`). |

Validation:
- Stream mode (`path:` unset) requires both `backup_<name>` and `restore_<name>`.
- Path mode (`path:` set) rejects bare `backup_<name>` / `restore_<name>`; use the `pre_`/`post_` wrappers.
- `cron` requires `keep`; `keep` is optional without `cron`.
- `keep` must be a positive integer.
- Each `timeout.<phase>` must parse as a Go duration string.
- Only standard five-field cron is accepted; macros like `@hourly` or `@reboot` are rejected at load.

```yaml
snapshots:
  postgres:
    cron: "0 3 * * *"
    keep: 7
    on_teardown: true
    timeout:
      step: 20m
  claude:
    path: /home/devm/.claude
    cron: "0 */4 * * *"
    keep: 7
```

Matching hooks in `devm.sh`:

```sh
backup_postgres()  { pg_dumpall -U postgres; }
restore_postgres() { psql -U postgres postgres; }

backup_buildkit()  { sudo tar c -C /var/lib/buildkit .; }
restore_buildkit() { sudo tar x -C /var/lib/buildkit; }
```

**Hook environment.** Hooks run inside the guest as the `devm` user with passwordless sudo available. Use `sudo` inside the hook body when the data you're capturing or restoring is root-owned (e.g. `/var/lib/buildkit`).

**Stream contract.** `backup_<name>` writes its payload to stdout; devm pipes that through `zstd` into the archive. `restore_<name>` reads the decompressed stream on its stdin. The hook author picks the on-wire format — `pg_dumpall` SQL, `tar`, a raw binary stream, anything else.

**Archive location.** Archives land on the Mac at `~/Library/Application Support/devm/<projectID>/snapshots/<name>/<ts>.zst` (`devm-e2e/` for the e2e identity). Retention (`keep:`) is enforced per entry after each successful capture.

**Cron scheduling.** Cron expressions are translated to a systemd `OnCalendar` timer installed in the guest. Five-field cron only; macros (`@hourly`, `@daily`, `@reboot`, …) are rejected.

**Lifecycle semantics.** On `devm stop` and `devm teardown`, entries with `on_stop: true` or `on_teardown: true` are captured serially in declaration order before the stop/teardown runs. Failures are collected — the remaining opted-in entries still run — and any failure aborts the stop/teardown. Pass `--skip-snapshot` on `devm stop` / `devm teardown` to skip the whole lifecycle pass.

**CLI.** `devm snapshot create|list|restore|prune|show <name>` from the Mac; `gdevm snapshot ...` from inside the guest.

---

## `cron`

`map[string]string` — bucket: **live**.

Runs `devm.sh` (or `devm.me.sh`) functions on a schedule inside the guest. Each key is a function name; each value is a five-field cron expression. `devm.me.yaml` can add entries or override one by key, like `env:`.

```yaml
cron:
  sync-gdevm-docs: "0 * * * *"
  prune-cache: "30 3 * * *"
```

Validation:
- The key must be a function defined in `devm.sh` or `devm.me.sh`.
- `install` and `startup` are rejected; devm already runs them.
- Only standard five-field cron is accepted; macros like `@hourly` or `@reboot` are rejected at load.

**Execution.** Each run is a systemd timer in the guest, as the `devm` user with the project's `env:` and `path:`, under the normal network allowlist, through the same wrapper as function-form services.

**Timing.** At boot, timers start only after services are up (after network enforcement). A run missed while the VM was stopped is skipped, and a run that would overlap a still-running one is skipped.

**Logs.** `journalctl -u devm-cron-<function>` in the guest.

**Applying changes.** Adding, removing, or changing an entry applies on `devm reconcile`; no restart.

---

## `base_image`

Object — bucket: **recreate**.

Accepted for YAML compatibility; has no active fields. Tart VM images are configured via the image pipeline, not per-project YAML flags. The block is an empty struct; structural equality means a devm.yaml edit cannot produce a detectable `base_image` change, so the recreate bucket entry for this field is unreachable in practice.

---

## See also

- `devm skills get devm` — `gdevm pop <path>` for opening a guest file on the Mac side from inside the VM.
- `devm skills get lifecycle` — how `devm reconcile` applies each bucket.

---

## Bucket glossary

**live** — Devm applies the change without stopping the VM or ending active sessions. Env, path, template, and package changes are applied directly inside the guest. `volumes:` and most of `repos:` (every field except `url`/`secret`) start, stop, or retarget a mutagen sync session without a VM cycle. Network (`allow`) and secret changes are applied by regenerating iron-proxy's config and respawning it — no VM restart, no prompt.

**restart** — VM stop + cold start, no teardown/data-loss. `devm reconcile` reports it as a distinct category from recreate, and the fix is `devm stop` + `devm start`. Sits here: `memory`/`cpu`/`swap` overrides, `docker.buildCache`, `snapshots:` entries, and `repos.<name>.url`/`repos.<name>.secret` (iron-proxy clones the repo at VM boot using these values). `devm.sh`/`devm.me.sh` edits (including `install()`/`startup()` bodies) aren't diffed by reconcile — see `devm skills get lifecycle` for how those actually propagate.

**recreate** — the VM must be fully deleted and recreated. `devm reconcile` prints the pending changes; a subsequent `devm start` performs the teardown and cold start. Fields in this bucket are baked in at VM creation time and cannot be patched onto a running VM: `base_image`, `project` identity fields, the `docker.enabled` toggle, and `disk` size.

---

<!-- migration-note-start -->
> **Migration note:** There is no legacy-key migration layer. Any key not in the current schema — including removed ones like `project.id`, `project.vm_name`, or `network.allowed_domains` — fails to load with an `unknown field` error listing the valid keys for that block.
<!-- migration-note-end -->
