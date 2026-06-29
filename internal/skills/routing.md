---
name: routing
description: devm route — making *.test domains resolve to your VM (or your Mac) for development. Covers the host proxy, the in-VM Caddy, dnsmasq, and the devm CA.
---

# devm routing reference

## The two destinations

`devm route local` points the daemon proxy at a service running on your Mac (the daemon proxies to `localhost:port`). `devm route vm` points the daemon proxy at a service running inside the Tart VM (the daemon proxies to `VM_IP:port`).

---

## `devm route local`

Run this from the project directory when your dev server runs on the Mac itself:

```
devm route local
```

What happens:

1. `devm` reads `devm.yaml` and collects every service that declares both `hostname` and `port`.
2. It sends a `POST /routes/apply` request to the daemon with `BackendHost=localhost` and `BackendPort=svc.Port` for each hostname.
3. The daemon's in-memory route table is updated immediately. No restart needed.

Subsequent HTTPS requests to `https://api.test` on the Mac hit the daemon proxy at :443, which looks up `api.test` in the route table and reverse-proxies to `localhost:3000` (or whatever port you declared).

### How `*.test` reaches the daemon on the Mac

`devm install` writes `/etc/resolver/test` with:

```
nameserver 127.0.0.1
port 51153
```

macOS's system resolver reads this file and forwards every `*.test` DNS query to the daemon's DNS server at `127.0.0.1:51153`. The daemon DNS server answers A queries with `127.0.0.1`, so your browser connects to the daemon proxy on :80 or :443.

The daemon proxy is a Go `httputil.ReverseProxy` that listens on :80 and :443 via launchd socket activation. It routes by the `Host:` request header. TLS is terminated by the daemon's built-in CA (see [The devm CA](#the-devm-ca) below).

---

## `devm route vm`

Run this from the project directory when your service runs inside the VM:

```
devm route vm
```

What happens:

1. `devm` queries Tart for the VM's current IP address (using `cfg.Project.VMName`). The VM must be running; start it first with `devm shell` if needed.
2. It sends `POST /routes/apply` to the daemon with `BackendHost=VM_IP` and `BackendPort=svc.Port` for each hostname.
3. The daemon proxy now dials `VM_IP:svc.Port` directly when a request arrives for that hostname.

The daemon does not use the in-VM Caddy for this path — it connects to the service's declared port on the VM's IP directly.

### Auto-routing on `devm shell`

`devm shell` automatically applies vm-mode routes when the project has no routes registered yet (best-effort, silent if the daemon is down). If you have already run `devm route local`, that routing is preserved across stop/start cycles and `devm shell` does not overwrite it.

---

## Clearing routes

Routes for a project are cleared when you run:

```
devm teardown
```

`teardown` sends `POST /routes/remove` to the daemon before stopping and deleting the VM. The route table is per-project: routes for other projects are not affected.

If you want to switch routing mode without tearing down (e.g., from `vm` to `local`), just re-run `devm route local` — applying new routes replaces the existing set for that project.

---

## Inside the VM: where does DNS go?

The VM's DNS and network stack is configured at boot by the daemon (via `tart exec` scripts). The effective setup:

1. **dnsmasq** runs at `127.0.0.1:53` inside the VM.
   - `address=/test/127.0.0.1` — any `*.test` query is answered locally with `127.0.0.1`.
   - Everything else is forwarded to iron-proxy's DNS listener at `MAC_HOST:DNSPort`.

2. **iron-proxy DNS** returns MAC_HOST's IP for every external name, so all outbound connections resolve to the Mac host and pass through the egress allowlist check.

3. **nftables** in the VM applies two rules to outbound TCP:
   - If the destination is already MAC_HOST, skip (prevents loops).
   - Port 80 → DNAT to `MAC_HOST:HTTPPort` (iron-proxy HTTP).
   - Port 443 → DNAT to `MAC_HOST:HTTPSPort` (iron-proxy HTTPS).
   - A filter chain default-denies everything except loopback and traffic to MAC_HOST on the proxy and DNS ports.

4. **Caddy** runs inside the VM (service `caddy`) with a generated `/etc/caddy/Caddyfile`. The Caddyfile has one `http://hostname { reverse_proxy localhost:port }` block per service that declares a hostname. `auto_https off` is set — TLS for in-VM access is handled at the Mac proxy layer. The Caddyfile is written by the provisioner at first boot and reloaded on `devm reconcile` when hostnames or ports change.

---

## The devm CA

The devm CA is a self-signed ECDSA root generated once at first daemon start and persisted at:

```
~/Library/Application Support/devm/ca/root.crt   (public)
~/Library/Application Support/devm/ca/root.key   (private)
```

`devm install` trusts the root in the macOS System Keychain:

```
security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain <path-to-root.crt>
```

This makes HTTPS to `*.test` names trust-chain-clean in browsers and `curl` without warnings.

The daemon proxy uses the CA's `GetCertificate` callback: leaf certs are signed on demand for whatever SNI the client sends (90-day validity, cached in memory, renewed automatically when fewer than 7 days remain before expiry).

At first boot, the provisioner also installs the CA root inside the VM:

```
/usr/local/share/ca-certificates/devm.crt
```

followed by `update-ca-certificates`, so tools running in the VM (curl, git, language runtimes) trust the CA for any HTTPS to `*.test` names that flow back through the Mac proxy.

---

## When to use which

| Situation | Command |
|---|---|
| API server running on your Mac (`go run ./cmd/api`) | `devm route local` |
| API server running as a systemd service inside the VM | `devm route vm` |
| Switching mid-session (e.g., moved the service into the VM) | Re-run `devm route vm`; it replaces the existing entry |
| Done with the project | `devm teardown` removes routes automatically |

---

## See also

- `devm skills get devm` — three-process model and top-level mental model.
- `devm skills get service` — daemon install, uninstall, and log locations.
- `devm skills get schema` — `service.hostname`, `service.port`, and `network.allow` fields.
- `devm skills get secrets` — declaring secrets and passing them through the proxy.
