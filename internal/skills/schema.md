---
name: schema
description: devm.yaml schema reference — every top-level field, type, and bucket semantics.
---

# devm.yaml schema reference

## Top-level fields

| Field | Type | Bucket | Purpose |
|---|---|---|---|
| `project` | object | recreate | Project identity and proxy settings (required). |
| `base_image` | object | recreate | Accepted for YAML compatibility; has no active fields. |
| `docker` | bool | recreate | Installs Docker in the VM and makes container egress trust iron-proxy transparently. Defaults `false`. |
| `network` | object | live | Iron-proxy outbound allowlist. |
| `env` | map[string]EnvValue | live | Project-wide environment variables forwarded to all services. |
| `services` | map[string]Service | varies | Named service definitions; bucket depends on which sub-field changes (see Services section). |
| `packages` | []string | recreate | Apt packages installed at VM creation time. |
| `install` | []string | recreate | Shell commands run once at VM creation as root. |
| `startup` | []string | restart | Shell commands run on every boot, in order, as root, with open network (before egress enforcement). The mechanism (`devm-startup.service` → `devm-enforce.service` ordering, nftables masked) is always registered, for every project. |
| `mounts` | []string | recreate | Host paths shared into the VM at matching absolute paths. |
| `path` | []string | live | Directories prepended to `$PATH` inside the VM. |
| `disk` | string | recreate | Override the guest's virtual disk size in GB (e.g. `"64GB"`). Defaults to 32 (baked into devm-base). tart's disk resize is grow-only, so values below 32 GB are rejected. |

---

## `project`

Required. Identifies the project and configures the local reverse proxy.

| Field | Type | Required | Purpose |
|---|---|---|---|
| `name` | string | yes | Project name. Serves as both the devm-owned identity namespace (secrets, routes, state, iron-proxy, ssh keys) and the literal Tart VM instance name. Must contain no whitespace, `/`, `\`, or `..`. |
| `proxy` | string | no | `caddy` (default) or `none`. With `none`, `devm route` subcommands print a disabled message and exit 0. |

Validation: `name` is required; `proxy` must be empty, `caddy`, or `none`.

Changing any `project` field is in the **recreate** bucket — the VM must be deleted and recreated from scratch.

---

## `network`

Controls outbound access enforced by iron-proxy (bucket: **live**).

| Field | Type | Purpose |
|---|---|---|
| `allow` | []AllowEntry | Hostnames the VM is permitted to reach, matched by SNI for TLS connections or HTTP Host header for plain HTTP. Each entry is a bare host scalar or a `{host, secrets}` mapping. |

Changes to `allow` take effect on the next `devm shell` cold start. The change-detection and live-apply path for network is not currently wired, so `devm reconcile` will not report or apply them.

Each allow entry accepts two forms:

- **Bare scalar** — just the hostname string: `- api.example.com`
- **Mapping** — `{host, secrets}`: names a host and lists which `!secret` values iron-proxy may inject on requests to that host only. Secrets not named in any allow entry are omitted from iron-proxy config and never injected.

Bare `*` is the open-egress sentinel: it matches any destination host, permitting unrestricted outbound access through iron-proxy.

```yaml
network:
  allow:
    - api.example.com                        # bare scalar
    - host: api.other.com
      secrets: [my_api_key]                  # inject my_api_key only to this host
    - "*"                                    # open egress — any host
```

---

## `env`

`map[string]EnvValue` — bucket: **live**.

Project-wide environment variables injected into all services. Values are literal strings or `!secret` references resolved from the macOS keychain:

```yaml
env:
  RAILS_ENV: development
  API_KEY: !secret my-api-key
```

Reserved keys (devm-injected; cannot be overridden): `WORKSPACE`, `IS_SANDBOX`.

Substitution rules in values:
- `$WORKSPACE` (or `${WORKSPACE}`) expands to the project root at load time.
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
- `$WORKSPACE` expands to the project root at load time. `$$` → literal `$`.
- Empty entries and `~` expansion are rejected.

---

## `install`

`[]string` — bucket: **recreate**.

Shell commands run once at VM creation time, in order, as root. Each command runs under `bash -o pipefail -c`. Bootstrap runs first, so `apt-get update` has already been called — user entries can `apt-get install -y <pkg>` directly.

`install` runs **once, on first boot only** — it is gated by a marker (`/var/lib/devm/provisioned`) and is **not** re-run on a later cold start (`devm stop` then `devm shell` reuses the same disk, so installed tools and built artifacts are still there). It runs with **open** network, before egress enforcement is applied. Use `install` for one-time setup. For a command that must run on **every** boot, use `startup:` (every boot, still open network — see below), or a service (`exec:` / `systemd:`) for a long-running process (every boot, under the enforced egress allowlist).

Changing `install` requires a full VM teardown and cold start (a fresh VM then re-runs first-boot `install` with the new commands).

Note: `--` in a command's argv is consumed by the internal wrapper; quote it or split the command into multiple steps.

---

## `startup`

`[]string` — bucket: **restart** (VM stop + cold start; no teardown, no data loss).

Shell commands run on **every** boot, in order, as root, with **open** network — before egress enforcement is applied. Rendered verbatim (one command per line, no shell escaping needed) into `/opt/devm/startup.sh` — mode 0755, root-owned — which the always-registered `devm-startup.service` executes (`Type=oneshot`, `RemainAfterExit=yes`), ordered `Before=devm-enforce.service`. Same open-network timing as `install:`, but every boot instead of once. Use it for per-boot setup that needs unrestricted network (fetch/refresh something, register the VM, warm a cache).

The mechanism is **always registered, for every project** — not opt-in on declaring `startup:`. devm always masks the firewall-first `nftables.service` and restores enforcement from `devm-enforce.service` ordered after `devm-startup.service`, so the boot order is `network → devm-startup.service (open egress) → devm-enforce.service (enforcement) → services` for every project, `startup:` set or not. An empty (or unset) `startup:` renders a no-op `startup.sh` (just the shebang + `set -eo pipefail`) that exits 0 immediately, so this window is negligible when there are no commands to run.

A failing `startup:` command does not block enforcement: `devm-enforce.service` only has `After=devm-startup.service` (no `Requires=`/`BindsTo=`), so systemd starts it regardless of whether `devm-startup.service` succeeded — enforcement is fail-safe.

The three hooks: `install:` = once, first boot, open network. `startup:` = every boot, open network. services (`exec:`/`systemd:`) = every boot, enforced egress (every declared service unit orders `After=devm-enforce.service`). Editing `startup:` commands only rewrites `/opt/devm/startup.sh`'s content — the unit itself is stable and never changes. The edit takes effect on the next `devm stop` + `devm shell` (restart bucket); devm does not re-run it live or mid-session.

---

## `mounts`

`[]string` — bucket: **recreate**.

Host paths shared into the VM via virtio-fs at matching absolute paths. Each entry is `HOST_PATH[:ro]`.

`HOST_PATH` may be:
- Absolute: `/Users/alice/src`
- Relative to the project root: `../shared`
- Home-relative: `~/data`

The optional `:ro` suffix makes the share read-only inside the VM.

Mounts are baked at `tart run` time; changing them requires a full VM teardown and cold start.

---

## `packages`

`[]string` — bucket: **recreate**.

Apt package names installed via `apt-get install -y` during VM creation. Changing this list requires a full VM teardown and cold start.

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
| `port` | int or "IP:PORT" | live | VM-side listen port. String form (`"0.0.0.0:8080"`) also sets the host bind IP; default bind is `127.0.0.1`. |
| `hostname` | string | live | Hostname for the Caddy reverse-proxy entry. Must end in `.test`. |
| `direct` | bool | live | Route this service directly to the VM's IP (DNS + firewall) instead of through the daemon HTTP proxy + in-VM Caddy. For raw-TCP / non-HTTP services (e.g. Postgres). Requires `hostname`. Default `false`. |
| `env` | map[string]EnvValue | live | Per-service environment variables (same `!secret` syntax as top-level `env`). |
| `masks` | []Mask | recreate | `mount --bind` overlays applied at boot. Each has `path` (relative to repo root) and `size` (e.g. `100m`). |
| `templates` | []Template | live | Files rendered from source scripts and written into the VM. Each has `source` (project-relative path), `output` (absolute path in VM), and optional `sudo` (default `false`; set `true` when `output` is under a root-owned dir like `/etc` so the installer escalates and the resulting file lands root-owned). |
| `exec` | []string | live | Command and arguments to run as the service process. |
| `workdir` | string | live | Working directory for the service process. |
| `restart` | string | live | Restart policy: `no`, `on-failure`, or `always`. |
| `after` | []string | live | Service names this service waits for at start (ordering only). |
| `user` | string | live | Unix user to run the service as. |
| `systemd` | string | live | Name of an existing systemd unit to manage. Mutually exclusive with `exec`, `restart`, `after`, `workdir`, and `user`. |

Validation rules:
- A service must define at least one of `port`, `masks`, `exec`, or `systemd`.
- `hostname` must end in `.test`.
- `direct: true` requires a `hostname`.
- Port values must be in range 1–65535; no two services may share a port or a hostname.
- Mask `path` must be relative to the repo root; absolute paths, `~`, `$VAR`, and `../` traversal are rejected.
- Template `source` must be project-relative (no `../` traversal); `output` must be absolute.
- Template `sudo` defaults to `false` (installer runs as the guest user, file lands owned by that user). Set `true` for outputs under `/etc`, `/usr`, `/var` where the guest user cannot write; the installer then uses `sudo install -o root -g root` and the file lands root-owned.

---

## `base_image`

Object — bucket: **recreate**.

Accepted for YAML compatibility; has no active fields. Tart VM images are configured via the image pipeline, not per-project YAML flags. The block is an empty struct; structural equality means a devm.yaml edit cannot produce a detectable `base_image` change, so the recreate bucket entry for this field is unreachable in practice.

---

## Bucket glossary

**live** — Devm applies the change to the running VM without stopping it or ending active sessions (env/path/template via a bundle re-pipe to `/opt/devm/`; service, port, and hostname via targeted `tart exec`). Network (`allow`) changes are classified live per the `changeBucket` map but the detection and apply path is not currently wired; they take effect on the next cold start.

**restart** (internally: `BucketRestartVM`, `String()` = `"restart"`) — VM stop + cold start, no teardown/data-loss; the provisioner re-establishes it next boot. `devm reconcile` reports it as a distinct category from recreate, and the fix is `devm stop` + `devm shell`. Currently only `startup:` sits in this bucket: an edit is re-rendered into `/opt/devm/startup.sh` by the bundle re-pipe, but only takes effect once `devm-startup.service` runs again on the next boot.

**recreate** (internally: `BucketTeardownVM`, `String()` = `"teardown"`) — the VM must be fully deleted and recreated. `devm reconcile` prints the pending changes; a subsequent `devm shell` performs the teardown and cold start. Fields in this bucket are baked in at VM creation time and cannot be patched onto a running VM: `install` commands, `packages`, `mounts` (virtio-fs shares set at `tart run` time), `masks` (bind mounts applied at boot), `base_image`, and `project` identity fields.

The classification of every change kind is the `changeBucket` map in `internal/reconcile/diff.go`.

---

<!-- migration-note-start -->
> **Migration note:** There is no legacy-key migration layer. Any key not in the current schema — including removed ones like `project.id`, `project.vm_name`, or `network.allowed_domains` — fails to load with an `unknown field` error listing the valid keys for that block.
<!-- migration-note-end -->
