---
name: schema
description: Reference — every field in devm.yaml and what it does.
hidden: true
---

# devm.yaml schema reference

Top-level fields (all optional unless noted):

| Field | Type | Purpose |
|---|---|---|
| `project` | object (REQUIRED) | `id`, `sandbox_name`, optional `port_offset`, `proxy`, `host_resolver` |
| `base_image` | object | `docker: bool` — true for the docker-templates:shell-docker image |
| `network` | object | `allowed_domains: [string]` — domain allowlist |
| `env` | map[string]string | Project-wide env vars. Substitution: `$WORKSPACE` expands to repo root. Reserved keys: `WORKSPACE`, `IS_SANDBOX`. |
| `services` | map[string]Service | Named service definitions. See Service fields below. |
| `install` | []string | Shell commands run ONCE at sandbox create as root. Each runs under `bash -o pipefail -c` (so pipelines fail loud). Wrapped by wrap-fg.sh. `apt-get update` already ran via bootstrap. |
| `mounts` | []string | Host paths mirrored into the sandbox at the same absolute path. Format: `HOST_PATH[:ro]`. |
| `path` | []string | Directories prepended to `$PATH` inside the sandbox. Final shape: `path[0]:path[1]:...:$WORKSPACE/.devm/scripts:$PATH`. Substitution: `$WORKSPACE` expands at load time. Entries must be absolute (start with `/` or `$WORKSPACE`); empty entries and `~` rejected. Reaches install, startup foreground, startup background, and the interactive shell via `.devm/.env`. **Bucket: live.** |

## Project fields

| Field | Type | Purpose |
|---|---|---|
| `id` | string (REQUIRED) | Project slug — used as the devm-owned namespace in shared resources (Caddy `@id`, etc.). |
| `sandbox_name` | string (REQUIRED) | Sbx sandbox name. |
| `port_offset` | int | Added to each service's canonical port for the host-side mapping. |
| `proxy` | string | `caddy` (default) or `none`. Gates `devm route`. With `none`, route subcommands print a disabled message and exit 0. |
| `host_resolver` | string | `snippet` (default) or `localias`. Selects how `/etc/hosts` is managed. `snippet` prints a copy-paste hint when hostnames don't resolve; `localias` talks to a running localias daemon. |

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
