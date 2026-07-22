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
| **Provisioner** | VM started; provisioning (cold start or warm restart) failed | Output contains `::devm:stage:<name>::` markers followed by `provision: provision stage "<name>": ...` |
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

After the VM starts, provisioning walks a series of stages. Each stage emits a marker (`::devm:stage:<name>::`) that drives the `devm shell` spinner. Egress is OPEN through the `open`→`startup` stages and switches to ENFORCED before `enforce`/`services`, so user services always come up under the enforced allowlist.

Any failing command aborts provisioning immediately. On failure the error line is:

```
provision: provision stage "<name>": provisioning script exited <N>
```

`<name>` is the LAST stage marker reached before the abort. `<N>` is the exit code — e.g. `124` means a `timeout`-wrapped `install:`/`startup:` command was killed for exceeding its budget. The output immediately above the error line is the captured non-marker stdout/stderr from the run; read that first.

### Stage reference

| Stage | What it does | Common failure | Fix |
|---|---|---|---|
| `open` | Marker for the start of the open-egress work window. Runs whenever there's open-window work to do (first boot, `startup:` commands, or templates); skipped on a warm restart with nothing to run. | Rare | n/a |
| `packages` | `apt-get update` + `apt-get install -y <packages>` (first boot only; skipped if `packages:` is empty) | Package name not found, or `deb.debian.org` not in `network.allow` | Add `deb.debian.org` to `network.allow` in `devm.yaml`; verify package names |
| `install` | Runs each `install:` command in order, with `$WORKSPACE`, `cfg.env`, and `path:` in scope (first boot only). Each command has a 600s timeout, overridable via `DEVM_INSTALL_STEP_TIMEOUT_S`. | User command exits non-zero, or a step exceeds its timeout (exit 124) | Read the captured output above the error; fix the failing command. For long installs, raise `DEVM_INSTALL_STEP_TIMEOUT_S`. |
| `docker` | Installs the Docker engine + shim (only when `docker: true`; first boot only) | Docker install failure | Read the captured output; if `network.allow` is missing a Docker registry host, add it |
| `templates` | Renders every `services[*].templates` entry into its declared output path. Runs on ANY boot that has templates declared. | Template output path unwritable (e.g. `/etc/foo` without `sudo: true` on the template) or template source render error | Add `sudo: true` to the template if the output is under a root-owned dir; otherwise fix the template source |
| `startup` | Runs every `startup:` command in one shared bash process (exports/`cd` persist between lines), wrapped in a single aggregate `timeout` — default 600s, overridable via `DEVM_INSTALL_STEP_TIMEOUT_S`. | A command exits non-zero, or the combined script exceeds its timeout | Read the captured output above the error; fix the failing command, or raise `DEVM_INSTALL_STEP_TIMEOUT_S` |
| `enforce` | Stage marker only — no in-guest work. Marks the point after which egress is enforced. | Should not fail | n/a |
| `services` | Applies per-service mask overlays, then enables + starts each declared service unit and health-polls it before `devm.target` (and therefore access) is granted. | Service failed to start (port in use, missing binary, bad config), or a mask's mount target doesn't exist | `tart exec <vm> systemctl status <unit>` and `tart exec <vm> journalctl -u <unit>`; check `services[*].masks` paths in `devm.yaml` |

A failure at `open` through `enforce` leaves the VM in a bad cold-start state — `devm shell` tears it down (the next `devm shell` starts clean). A failure at `templates` or `services` leaves a basically-good VM whose user-declared service/template is what's broken, so `devm shell` surfaces the error but keeps the VM running for in-place debugging via `tart exec`.

---

## Workload failures

These occur after a successful provision: the VM is up and provisioned, but code running inside it cannot reach something.

### Connection refused or no such host

```
curl: (7) Failed to connect to api.example.com port 443: Connection refused
curl: (6) Could not resolve host: api.example.com
```

Under the enforced egress policy, only HTTPS (:443), HTTP (:80), and NTP (:123) leave the VM. HTTP/HTTPS goes through iron-proxy on the Mac, which applies `network.allow`. Two things can produce a connection-refused symptom:

1. **iron-proxy blocked it.** The destination is not listed under `network.allow` in `devm.yaml`, so iron-proxy dropped the request. Fix: add the host to `network.allow` (or change to `- "*"` for open egress if you actually want no allowlist).
2. **The workload tried a non-HTTP port.** Outbound to a port other than 80 or 443 (e.g. 5432, 3306, 6379 for talking to external Postgres/MySQL/Redis) is dropped before iron-proxy sees it — there's nothing to add to `network.allow` that would help. Fix: front the external service with an HTTPS endpoint, or (for peer VMs on the same Mac) use `direct: true` on the target service and address it by hostname.

Check:

1. `devm.yaml` — confirm the host appears under `network.allow`.
2. Iron-proxy log at `~/Library/Logs/devm/<project-id>-proxy.log` — logs every :80/:443 request decision. Replace `<project-id>` with the value of `project.name` in your `devm.yaml`. If the failing flow isn't logged there at all, it was dropped before iron-proxy saw it (case 2 above).

See `devm skills get routing` for `network.allow` syntax and how routing works.

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
| Iron-proxy | `~/Library/Logs/devm/<project-id>-proxy.log` | Per-project; logs every proxied request. `<project-id>` = `project.name` in `devm.yaml` |
| In-VM systemd | `tart exec <vm> journalctl -u <unit>` | Use `-xe` for recent system errors; use `-u <unit>` for a specific service |
| Install / uninstall | `~/Library/Logs/devm/install.log` | Subprocess output from `devm install` and `devm uninstall`; last 30 lines are printed automatically on failure |

The `.devm/` directory in your project root is maintained by the CLI and is not committed to version control. It contains:

- `.devm/.env` — rendered environment file; shell-sourceable; sourced by the VM shell on attach
- `.devm/templates/` — installer scripts generated from `devm.yaml` template declarations
