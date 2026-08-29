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
| `docker` | bool | recreate | Installs Docker in the VM and makes container egress trust iron-proxy transparently. Defaults `false`. |
| `network` | object | live | Iron-proxy outbound allowlist. |
| `env` | map[string]EnvValue | live | Project-wide environment variables forwarded to all services. |
| `services` | map[string]Service | varies | Named service definitions; bucket depends on which sub-field changes (see Services section). |
| `packages` | []string | live | Apt packages. A running VM converges via a transient egress window; a stopped VM converges on the next boot's open window. |
| `install` | []string | recreate | Shell commands run once at VM creation as the guest `devm` user. NOPASSWD sudo is available for privileged steps. |
| `startup` | []string | restart | Shell commands run on every boot that opens the egress window (first boot, or `startup:` itself non-empty, or any service declares `templates:`), in order, as the guest `devm` user, with open network — before egress enforcement is applied. NOPASSWD sudo is available for privileged steps. |
| `scripts` | map[string][]string | (see below) | Named library of reusable multi-command shell snippets, referenced from `install:`/`startup:` via a `>NAME` entry. |
| `volumes` | map[string]Volume | live | Per-project named persistent stores. Key = volume name; value is either a bare guest path string or a `{path, label, ignore}` mapping. Data lives on the Mac side under `~/Library/Application Support/devm/<projectID>/<label>/` and survives `devm teardown`. See the `volumes` section below. |
| `repos` | map[string]RepoConfig | varies | Declares the project's git repos to hydrate via `git clone` at cold-start, keyed by an arbitrary schema id. Exactly one entry is the primary workspace repo. `url`/`secret` are **restart-VM** (iron-proxy clones at boot using these); every other field is **live** (mutagen-session-only). Optional — omit for utility VMs with no repo. See the `repos` section below. |
| `path` | []string | live | Directories prepended to `$PATH` inside the VM. |
| `disk` | string | recreate | Override the guest's virtual disk size in GB (e.g. `"64GB"`). Defaults to 32 (baked into devm-base). tart's disk resize is grow-only, so values below 32 GB are rejected. |
| `memory` | string | restart | Override the VM's RAM, e.g. `"8G"`, `"16G"`. Unset uses the base image default. Requires a G/GB suffix; the magnitude must be a positive integer. Removing this field from devm.yaml does not revert the running VM's tart config; the previously-set value persists across reconcile-restarts. Use `devm teardown` to fully reset to the base image default. Not overridable in `devm.me.yaml`. |
| `cpu` | int | restart | Override the VM's virtual CPU count. Unset uses the base image default. Must be a positive integer. Removing this field from devm.yaml does not revert the running VM's tart config; the previously-set value persists across reconcile-restarts. Use `devm teardown` to fully reset to the base image default. Not overridable in `devm.me.yaml`. |

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

On a running VM, `devm reconcile` applies `allow` changes automatically by regenerating iron-proxy's config and respawning it — no VM restart, no prompt. On a stopped VM, changes take effect at the next `devm shell` cold start.

Each allow entry accepts two forms:

- **Bare scalar** — just the hostname string: `- api.example.com`
- **Mapping** — `{host, secrets}`: names a host and lists which `!secret` values iron-proxy may inject on requests to that host only. Secrets not named in any allow entry are omitted from iron-proxy config and never injected.

Bare `*` is the open-egress sentinel: it matches any destination host, permitting unrestricted outbound access through iron-proxy.

A host may carry a path pattern: everything from the first `/` scopes reachability to matching request paths. A pattern ending `/*` matches the whole subtree; anything else must match exactly. Query strings never participate in matching — drop them. Secrets on a path-scoped entry still inject host-wide.

```yaml
network:
  allow:
    - api.example.com                        # bare scalar — whole host
    - cdn.example.com/dl/v2/*                # only this path subtree
    - host: api.other.com
      secrets: [my_api_key]                  # inject my_api_key only to this host
    - "*"                                    # open egress — any host
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

Reserved keys (devm-injected; cannot be overridden): `WORKSPACE`, `IS_SANDBOX`.

Substitution rules in values:
- `$WORKSPACE` (or `${WORKSPACE}`) expands to the primary repo's GUEST path (`/home/devm/<primary-label>`), or `/home/devm` when no repos are declared. Substitution happens at load time on the Mac; the expanded value lands in `/etc/environment` inside the guest, so `$WORKSPACE` in scripts resolves the same way at guest-shell run time.
- `$$` → literal `$`.
- Any other `$VAR` reference is an error.

Per-service `env` entries win over top-level `env` on key collision.

Note: `devm reconcile` detects env changes via per-service `env` entries only. A change to top-level `env` with no corresponding per-service change produces no diff output; it takes effect on the next `devm shell` cold start.

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

## `install`

`[]string` — bucket: **recreate**.

Shell commands run once at VM creation time, in order, as the guest `devm` user. Each command runs under `bash -o pipefail -c`. Bootstrap runs first, so `apt-get update` has already been called — user entries can `sudo apt-get install -y <pkg>` directly (the `devm` user has NOPASSWD sudo baked into the base image).

`install` runs **once, on first boot only** — it is gated by a marker (`/var/lib/devm/provisioned`) and is **not** re-run on a later cold start (`devm stop` then `devm shell` reuses the same disk, so installed tools and built artifacts are still there). It runs with **open** network, before egress enforcement is applied. Use `install` for one-time setup. For a command that must run on **every** boot, use `startup:` (every boot, still open network — see below), or a service (`exec:` / `systemd:`) for a long-running process (every boot, under the enforced egress allowlist).

Changing `install` requires a full VM teardown and cold start (a fresh VM then re-runs first-boot `install` with the new commands).

Note: `--` in a command's argv is consumed by the internal wrapper; quote it or split the command into multiple steps.

---

## `startup`

`[]string` — bucket: **restart** (VM stop + cold start; no teardown, no data loss).

Shell commands run on **every** boot where the open-egress window runs, in order, as the guest `devm` user, with **open** network — before egress enforcement is applied. NOPASSWD sudo is available for privileged steps. Runs under one shared shell (exports/`cd` persist between lines), 600s timeout for the whole block (override with `DEVM_INSTALL_STEP_TIMEOUT_S`). Use it for per-boot setup that needs unrestricted network (fetch/refresh something, register the VM, warm a cache).

The open-egress window itself only runs when there's work for it: first boot, or `startup:` is non-empty, or any service declares `templates:`. A project with no `startup:` and no `templates:` skips the window entirely on a later cold start and goes straight to the enforced allowlist.

A failing `startup:` command aborts provisioning: `devm.target` never starts, no access is granted, and the VM is torn down — same failure class as a broken `install:` command, not fail-safe.

The three hooks: `install:` = once, first boot, open network. `startup:` = every boot that opens the window, open network. Services (`exec:`/`systemd:`) = every boot, enforced egress — started and health-polled after the allowlist is applied, confirmed healthy before `devm.target` (and therefore access) comes up. Editing `startup:` (**restart** bucket) is deterministic: the freshly-rendered `startup:` runs on the applying `devm stop` + `devm shell` — the edit takes effect on that restart.

---

## `scripts`

`map[string][]string` — bucket: none of its own; a change only takes effect through whichever hook references it (`install:` → recreate, `startup:` → restart).

A named library of reusable multi-command shell snippets. Each key is the script name and must match `[a-z][a-z0-9-]*` (kebab-case, starting with a letter). Each value is an ordered list of shell commands.

Reference a script from `install:` or `startup:` with a single string entry of the form `>NAME`:

```yaml
scripts:
  install-supabase:
    - curl -fsSL https://example.com/install.sh -o /tmp/install.sh
    - bash /tmp/install.sh
install:
  - ">install-supabase"
```

When referenced from `install:`, the engine joins the script's commands with ` && ` and runs them under one `bash -eo pipefail -c` invocation. When referenced from `startup:`, the commands are emitted inline into `startup.sh` (which already runs under one shared shell), so variables set in one step are visible in later steps.

V1 scope: refs are only resolved from `install:` and `startup:`. Scripts take no parameters and cannot call other scripts.

---

## `packages`

`[]string` — bucket: **live**.

Apt package names installed via `apt-get install -y`. Adding or removing entries converges without a teardown: on a running VM, the daemon briefly respawns iron-proxy with the apt mirrors added to the allowlist (`deb.debian.org`, `security.debian.org`, plus `download.docker.com` when `docker: true`), runs the apt diff, then restores the original allowlist. On a stopped VM, the same diff converges during the next boot's open egress window. Reordering the list is a no-op — only membership changes trigger a converge.

```yaml
packages:
  - postgresql-client
  - redis-tools
```

Note: if `packages` is empty, `apt-get update` is skipped entirely during provisioning.

---

## `services`

`map[string]Service` — bucket varies by sub-field.

Named service definitions. Each key is the service name.

| Field | Type | Bucket | Purpose |
|---|---|---|---|
| `port` | int or "IP:PORT" | live | VM-side listen port. String form (`"0.0.0.0:8080"`) is parsed for a bind IP, but that IP is silently ignored: every service's host-side listener binds on the project's allocated `127.42.0.N` address (its own loopback IP, one per project). For LAN reachability of a service's hostname, use `expose_host: true` (below) — that opts the hostname into devm's shared LAN dispatcher on `0.0.0.0:42000`, which is the correct mechanism. |
| `hostname` | string | live | Hostname this service answers to, dispatched by the daemon's ProxyServer (from the Mac) and its guest-origin listener (from inside the VM). Must end in `.test`. |
| `direct` | bool | live | Treat this service as raw TCP end-to-end instead of HTTP-fronted: skips the daemon's ProxyServer hostname dispatch and its guest-origin listener. DNS still answers `hostname` with the project's `127.42.0.N` on the Mac (and `127.0.0.1` inside the VM), and the client connects directly — every service with `hostname` + `port` is exposed on softnet regardless of `direct`; this flag only controls how it's fronted. Use for non-HTTP protocols (Postgres, gRPC) a reverse proxy can't front. Requires `hostname`. Default `false`. |
| `expose_host` | bool | live | Opt this service's `hostname` into devm's shared LAN dispatcher (host `0.0.0.0:42000`), so other devices on the LAN can reach it by hostname. Requires `hostname`. Independent of `direct`. Default `false`. |
| `env` | map[string]EnvValue | live | Per-service environment variables (same `!secret` syntax as top-level `env`). |
| `templates` | []Template | live | Files rendered from source scripts and written into the VM. Each has `source` (project-relative path), `output` (absolute path in VM), and optional `sudo` (default `false`; set `true` when `output` is under a root-owned dir like `/etc` so the installer escalates and the resulting file lands root-owned). |
| `exec` | []string | live | Command and arguments to run as the service process. |
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
| `commands` | map[string]RepoCommand | no | Named commands for this repo, keyed by command name (`^[a-z][a-z0-9_-]*$`). Each entry has `exec` (required — a literal shell command, or a `>NAME` reference into top-level `scripts:`) and `startup` (bool, defaults false). |

**Primary determination** — exactly one of these must hold across `repos`:
- one entry sets `primary: true` explicitly, or
- exactly one entry omits `url:` (that omission implies it's primary, deriving its URL from `git remote get-url origin`).

Zero or multiple explicit `primary: true` entries, or zero or multiple `url`-omitted entries, is a validation error at load time. Non-primary entries must always declare `url:` explicitly.

**Validation**:
- **Label collisions**: every `repos` and `volumes` entry resolves to a label (explicit `label:`, or its derived default). Two entries — repo-repo, repo-volume, or volume-volume — resolving to the same label is rejected; set an explicit `label:` on one to disambiguate.
- **Reserved project names**: `project.name` may not collide with a devm-internal storage directory name (`bin`, `state`, `iron-proxy`, `mutagen`, `volumes`) — repo and volume storage lives under the project's own directory, and a name collision would shadow devm's internals.

---

## `base_image`

Object — bucket: **recreate**.

Accepted for YAML compatibility; has no active fields. Tart VM images are configured via the image pipeline, not per-project YAML flags. The block is an empty struct; structural equality means a devm.yaml edit cannot produce a detectable `base_image` change, so the recreate bucket entry for this field is unreachable in practice.

---

## See also

- `devm skills get devm` — `devm pop mac`/`devm pop vm` for navigating between a guest-side path and its Mac-side volume storage.
- `devm skills get lifecycle` — how `devm reconcile` applies each bucket.

---

## Bucket glossary

**live** — Devm applies the change without stopping the VM or ending active sessions. Env, path, template, and package changes are applied directly inside the guest. `volumes:` and most of `repos:` (every field except `url`/`secret`) start, stop, or retarget a mutagen sync session without a VM cycle. Network (`allow`) and secret changes are applied by regenerating iron-proxy's config and respawning it — no VM restart, no prompt.

**restart** — VM stop + cold start, no teardown/data-loss. `devm reconcile` reports it as a distinct category from recreate, and the fix is `devm stop` + `devm shell`. Sits here: `startup:` (edit takes effect on the applying restart) and `repos.<name>.url`/`repos.<name>.secret` (iron-proxy clones the repo at VM boot using these values).

**recreate** — the VM must be fully deleted and recreated. `devm reconcile` prints the pending changes; a subsequent `devm shell` performs the teardown and cold start. Fields in this bucket are baked in at VM creation time and cannot be patched onto a running VM: `install` commands, `base_image`, and `project` identity fields.

---

<!-- migration-note-start -->
> **Migration note:** There is no legacy-key migration layer. Any key not in the current schema — including removed ones like `project.id`, `project.vm_name`, or `network.allowed_domains` — fails to load with an `unknown field` error listing the valid keys for that block.
<!-- migration-note-end -->
