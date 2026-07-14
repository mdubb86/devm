---
name: errors
description: Debugging devm — failure shapes from the daemon, the provisioner, and the VM. Use when devm shell fails to come up, or when something inside the VM isn't reachable.
---

# Debugging devm

Failures fall into three layers. Identifying the layer narrows the search to the right log.

## Three layers of failure

| Layer | When it fails | Symptom |
|---|---|---|
| **Daemon** | Before the VM starts | `devm shell` prints `query vm status: ...` or another pre-VM error |
| **Provisioner** | VM started; first-boot setup failed | Output contains `[step: <name>]` lines followed by `provision: provision step "<name>": ...` |
| **Workload** | Provision succeeded; code in the VM can't reach something | Connection refused, DNS failure, or HTTPS cert error inside the VM |

---

## Daemon failures

`devm shell` contacts the daemon over a local socket before doing anything else. If the daemon is not running:

```
query vm status: <detail>
```

Check daemon state:

```
devm service status
```

Prints `running`, `stopped`, or `not installed`. If stopped:

```
devm service start
```

If the daemon fails to stay up, check the error log:

```
tail -n 50 ~/Library/Logs/com.devm.service.err.log
```

Other pre-VM errors from `devm shell`:

| Error prefix | Cause | Fix |
|---|---|---|
| `render devm dir: ...` | `devm.yaml` failed to render (bad template variable or YAML parse error) | Fix the YAML and retry |
| `resolve secrets: missing secrets in keychain: [<name>] ...` | A `!secret` reference has no matching entry in the macOS login keychain | Run `devm secret set <name>` for each listed name; see `devm skills get secrets` |
| `start vm: ...` | Daemon rejected the `StartVM` call | Check daemon log at `~/Library/Logs/com.devm.service.err.log` |
| `vm did not become ready: timeout waiting for vm <name> to become exec-ready` | VM did not accept exec connections within 60 seconds | Run `tart list` to check VM state; daemon log may have more detail |

---

## Provisioner failures

After the VM starts, `devm shell` runs a 13-step provisioner inside the VM. Each step prints a header as it begins:

```
[step: <name>]
<stdout and stderr from the command>
```

On failure the error line is:

```
provision: provision step "<name>": tart exec <command>: exit <N>
```

The output block immediately above the error line contains the captured stdout and stderr from the failing command. Read that block first.

### Step reference

| Step | What it does | Common failure | Fix |
|---|---|---|---|
| `mkdir workspace parents` | `sudo mkdir -p <parent-of-workspace-path>` inside the VM | VM user cannot create the path (permissions or path component missing) | The workspace path is mounted from the Mac host into the VM at the same absolute path; the path is set via `mounts:` in `devm.yaml` (or the daemon's default `WorkspaceHostPath`). Verify that path exists on the host and check base image sudo configuration. |
| `install devm bundle` | Builds the devm-owned artifact bundle (env file, with-devm-env wrapper, install-templates.sh dispatcher, systemd units, Caddyfile, dnsmasq config, install.sh) and pipes it into the guest, where `install.sh` extracts it to `/opt/devm`, installs systemd units to `/etc/systemd/system/`, writes configs to `/etc/caddy/` and `/etc/dnsmasq.d/`, and symlinks `with-devm-env` onto `/usr/local/bin`. Systemd units are rendered from `services[*]` with merged cfg-level and per-service env. | Rare — pipe interrupted, or `sudo`/`/usr/local/bin` unavailable in the guest. Bad `services[*].exec` or `services[*].systemd` field in `devm.yaml` causes unit file syntax errors. | Should not fail on a healthy base image; check base image integrity. Verify `services[*]` declarations in `devm.yaml`. |
| `reload base services` | `systemctl reload-or-restart dnsmasq` then `systemctl reload-or-restart caddy` | dnsmasq: port 53 is already bound (e.g., `systemd-resolved` is active in the VM). Caddy: Caddyfile syntax error | dnsmasq: `tart exec <vm> journalctl -u dnsmasq`. Caddy: `tart exec <vm> journalctl -u caddy` |
| `apt-get update` | `sudo apt-get update -y` (skipped if `packages:` is empty in `devm.yaml`) | `deb.debian.org` is not listed in `network.allow`; apt hangs or fails | Add `deb.debian.org` to `network.allow` in `devm.yaml` |
| `apt-get install packages` | Installs every package listed under `packages:` | Package name not found, or apt network access blocked | Read the captured apt output; verify package names and check `network.allow` |
| `scaffold user firewall chain` | Sets up per-project iptables/nftables chain for user-supplied firewall rules | Script failure or nftables unavailable | Should not fail on a healthy base image with kernel nftables support |
| `run install commands` | Runs each command listed under `install:` in order; prints `[N/M] <command>` before each one. Each command goes through the with-devm-env wrapper so `$WORKSPACE`, `cfg.env`, and `path:` are in scope; per-step timeout defaults to 600s, overridable via `DEVM_INSTALL_STEP_TIMEOUT_S`. | User command exits non-zero, or step exceeds the timeout | Read the captured stderr shown in the output block above the error; fix the failing command. For long installs, raise `DEVM_INSTALL_STEP_TIMEOUT_S`. |
| `docker feature` | Sets up Docker socket forwarding and systemd user service (only when `docker: true` in `devm.yaml`) | docker-proxy or docker socket unavailable on the Mac | Verify `docker: true` is set and Docker is running on the Mac |
| `install templates` | Runs `install-templates.sh` inside the VM to render every `services[*].templates` entry into its declared output path. Uses `sudo install -o root -g root` for entries with `sudo: true`; plain `mv` (guest-user-owned) otherwise. | Template output path unwritable (e.g. `/etc/foo` without `sudo: true` on the template) or template source render error | Add `sudo: true` to the template if the output is under a root-owned dir; otherwise fix the template source |
| `systemctl daemon-reload` | Reloads systemd after bundle systemd units are installed | Unit file syntax error | Run `tart exec <vm> journalctl -xe` for the systemd error detail; verify `services[*].exec` and `services[*].systemd` in `devm.yaml` |
| `apply egress enforcement` | Injects iron-proxy nftables and dnsmasq configuration to enforce outbound network policy | Script failure or nftables unavailable | Should not fail on a healthy base image; check nftables kernel support |
| `enable + start services` | `sudo systemctl enable --now <unit>` for each service that declares an exec or systemd unit | Service failed to start (port in use, missing binary, bad config) | `tart exec <vm> systemctl status <unit>` and `tart exec <vm> journalctl -u <unit>` |
| `apply masks` | Bind-mounts per-service mask directories from `/var/devm/masks/<project-id>/<service>/<path>` over workspace paths | Target path doesn't exist, or `mount --bind` fails | Check `services[*].masks` paths in `devm.yaml`; verify workspace path is correct |

---

## Workload failures

These occur after a successful provision: the VM is up and provisioned, but code running inside it cannot reach something.

### Connection refused or no such host

```
curl: (7) Failed to connect to api.example.com port 443: Connection refused
curl: (6) Could not resolve host: api.example.com
```

All outbound traffic from the VM is routed through iron-proxy on the Mac. If the destination is not listed under `network.allow` in `devm.yaml`, iron-proxy blocks it. Check:

1. `devm.yaml` — confirm the host appears under `network.allow`.
2. Iron-proxy log at `~/Library/Logs/devm/<project-id>-proxy.log` — logs every request decision. Replace `<project-id>` with the value of `project.id` in your `devm.yaml`.

See `devm skills get routing` for the full iron-proxy network model and allow-list syntax.

### HTTPS certificate errors

```
curl: (60) SSL certificate problem: unable to get local issuer certificate
```

Iron-proxy terminates TLS on the Mac and re-signs responses with the devm CA. If the VM does not trust the devm CA, every HTTPS request through the proxy fails with a cert error.

Check the `install devm bundle` step output from the most recent cold start. CA cert installation happens during that step; any CA merge failure surfaces as a `FAIL: devm CA installed to CApath but not merged into ca-certificates.crt bundle` line. If it failed, recreate the VM (delete and re-run `devm shell`) so the provisioner runs again.

If the CA cert itself is missing from the Mac (`~/Library/Application Support/devm/ca/root.crt`), or is not trusted in the System Keychain, run `devm install` to regenerate and re-trust it.

### Token and secret issues

If an API call fails with unexpected credentials or a 401, check whether iron-proxy's token substitution is working correctly. See `devm skills get secrets` for the full secret flow and how to diagnose substitution failures.

---

## Where logs live

| Log | Path | Notes |
|---|---|---|
| Daemon stdout | `~/Library/Logs/com.devm.service.out.log` | Primary daemon output |
| Daemon stderr | `~/Library/Logs/com.devm.service.err.log` | Start here for daemon crashes and startup failures |
| Iron-proxy | `~/Library/Logs/devm/<project-id>-proxy.log` | Per-project; logs every proxied request. `<project-id>` = `project.id` in `devm.yaml` |
| In-VM systemd | `tart exec <vm> journalctl -u <unit>` | Use `-xe` for recent system errors; use `-u <unit>` for a specific service |
| Install / uninstall | `~/Library/Logs/devm/install.log` | Subprocess output from `devm install` and `devm uninstall`; last 30 lines are printed automatically on failure |

The `.devm/` directory in your project root is maintained by the CLI and is not committed to version control. It contains:

- `.devm/.env` — rendered environment file; shell-sourceable; sourced by the VM shell on attach
- `.devm/templates/` — installer scripts generated from `devm.yaml` template declarations
