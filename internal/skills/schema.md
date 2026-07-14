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
| `mounts` | []string | recreate | Host paths shared into the VM at matching absolute paths. |
| `path` | []string | live | Directories prepended to `$PATH` inside the VM. |
| `disk` | string | recreate | Override the guest's virtual disk size in GB (e.g. `"64GB"`). Defaults to 32 (baked into devm-base). tart's disk resize is grow-only, so values below 32 GB are rejected. |

---

## `project`

Required. Identifies the project and configures the local reverse proxy.

| Field | Type | Required | Purpose |
|---|---|---|---|
| `id` | string | yes | Project slug used as the devm-owned namespace in shared resources (Caddy `@id`). |
| `vm_name` | string | yes | Tart VM instance name (the running VM identifier). |
| `proxy` | string | no | `caddy` (default) or `none`. With `none`, `devm route` subcommands print a disabled message and exit 0. |

Validation: `id` and `vm_name` are required; `proxy` must be empty, `caddy`, or `none`.

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

Changing `install` requires a full VM teardown and cold start.

Note: `--` in a command's argv is consumed by the internal wrapper; quote it or split the command into multiple steps.

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

**recreate** (internally: `teardown+shell`) — the VM must be fully deleted and recreated. `devm reconcile` prints the pending changes; a subsequent `devm shell` performs the teardown and cold start. Fields in this bucket are baked in at VM creation time and cannot be patched onto a running VM: `install` commands, `packages`, `mounts` (virtio-fs shares set at `tart run` time), `masks` (bind mounts applied at boot), `base_image`, and `project` identity fields.

The classification of every change kind is the `changeBucket` map in `internal/orchestrator/diff.go`.

---

<!-- migration-note-start -->
> **Migration note:** Configs that use `network.allowed_domains:` or `project.sandbox_name:` will fail to load with a specific error message pointing to the replacement key (`network.allow` and `project.vm_name`, respectively).
<!-- migration-note-end -->
