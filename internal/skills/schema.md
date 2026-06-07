---
name: schema
description: Reference — every field in devm.yaml and what it does.
hidden: true
---

# devm.yaml schema reference

Top-level fields (all optional unless noted):

| Field | Type | Purpose |
|---|---|---|
| `project` | object (REQUIRED) | `id`, `sandbox_name`, `hostname_apex`, optional `port_offset` |
| `base_image` | object | `docker: bool` — true for the docker-templates:shell-docker image |
| `network` | object | `allowed_domains: [string]` — domain allowlist |
| `env` | map[string]string | Project-wide env vars. Substitution: `$WORKSPACE` expands to repo root. Reserved keys: `WORKSPACE`, `IS_SANDBOX`. |
| `services` | map[string]Service | Named service definitions. See Service fields below. |
| `install` | []string | Shell commands run ONCE at sandbox create as root. Each runs under `bash -o pipefail -c` (so pipelines fail loud). Wrapped by wrap-fg.sh. `apt-get update` already ran via bootstrap. |
| `mounts` | []string | Host paths mirrored into the sandbox at the same absolute path. Format: `HOST_PATH[:ro]`. |

## Service fields

| Field | Type | Purpose |
|---|---|---|
| `port` | int OR "IP:N" | Sandbox-side port. String form sets bind IP. |
| `hostname` | string | Hostname for Caddy reverse-proxy entry. |
| `env_inject` | bool | If true, exports NAME_PORT/NAME_HOST env vars from this service. |
| `env_host` | string | The IP to inject as NAME_HOST. Requires env_inject. |
| `env` | map[string]string | Per-service env. Flattened with NAME_ prefix. |
| `masks` | []Mask | tmpfs overlay masks. `path` + `size`. |
| `templates` | []Template | `source` (relative file) + `output` (absolute path in VM). |
| `startup` | []StartupCommand | Per-service startup commands. Each has `command: []string`, optional `background: bool`. |

## Affordances baked into bootstrap

- `apt-get update` runs first; user install steps can `apt-get install -y <pkg>` directly.
- `ncurses-term` is pre-installed (modern terminfo for TUIs).
- `s6-log` is dropped at `.devm/scripts/s6-log` (used by wrap-bg.sh for background daemon log rotation).

## Reserved names

- `env.WORKSPACE`, `env.IS_SANDBOX` — devm-injected, cannot be user-set.
- `$WORKSPACE` in env values — expands to repo root at load time.
- `$$` in env values — escapes to literal `$`.
- `--` in install / startup command argv — reserved by the wrap-fg.sh / wrap-bg.sh wrappers.
