---
name: routing
description: devm routing — making *.test domains reach your VM's services (or your Mac's) for development. Covers `devm route local`/`vm`, direct services, the devm CA, and the two Mac-side proxies (the daemon's built-in ProxyServer for `.test` ingress and iron-proxy for VM-outbound allowlist enforcement).
---

# devm routing reference

## The two destinations

`devm route local` and `devm route vm` both configure the **daemon's built-in ProxyServer** (a reverse proxy the devm daemon runs on `127.42.0.N:80/443` for each project). They only differ in what the ProxyServer dials as its upstream: `local` = `localhost:port` on the Mac; `vm` = the project's own `127.42.0.N:port` (which softnet forwards to the guest). This is a different process from **iron-proxy** — iron-proxy is a per-project subprocess that handles VM **outbound** traffic (allowlist + secret injection); it is NOT involved in client-side ingress to `*.test` hostnames.

---

## `devm route local`

Run this from the project directory when your dev server runs on the Mac itself:

```
devm route local
```

`devm` reads `devm.yaml`, collects every service that declares both `hostname` and `port`, and sends the routes to the daemon. The daemon's in-memory route table is updated immediately — no restart needed.

Subsequent HTTPS requests to `https://api.test` on the Mac hit the daemon's ProxyServer, which looks up `api.test` and reverse-proxies to `localhost:3000` (or whatever port you declared).

### How `*.test` reaches the daemon proxy on the Mac

`devm install` writes `/etc/resolver/test` so macOS's system resolver forwards every `*.test` DNS query to the devm daemon's DNS server. Each running project is allocated its own address from the `127.42.0.1..20` loopback pool; the daemon answers that project's `*.test` A queries with its own `127.42.0.N`, so two projects that both expose `db.test` on 5432 don't collide — each project's `db.test` resolves to a different IP. The daemon's ProxyServer binds each project's `:80`/`:443` on its own `127.42.0.N` and dispatches by `Host:` header, terminating TLS with the devm CA (see [The devm CA](#the-devm-ca) below).

A query for an unknown hostname, or for a project that isn't currently running, gets NXDOMAIN.

---

## `devm route vm`

Run this from the project directory when your service runs inside the VM:

```
devm route vm
```

`devm` sends the routes to the daemon; for each non-direct service, the daemon substitutes `BackendHost = 127.42.0.N` (the project's allocated loopback IP) at apply time — softnet has bound the service's port on that address, so the ProxyServer's dial reaches the guest via softnet's forward.

If the VM isn't running yet (no `127.42.0.N` allocated for this project), `devm route vm` fails loudly with an error naming the project and pointing at `devm start` — the daemon rejects the apply with `no projectIP allocated for "<project>" — start the VM first: \`devm start\``, which the CLI surfaces wrapped as `apply routes: routes/apply: status 400: ...`.

### Auto-routing on `devm shell`

`devm shell` automatically applies vm-mode routes when the project has no routes registered yet (best-effort, silent if the daemon is down). If you have already run `devm route local`, that routing is preserved across stop/start cycles and `devm shell` does not overwrite it.

---

## Direct services (`direct: true`)

A service with `direct: true` is reached **directly on the project's `127.42.0.N`**, bypassing the daemon's ProxyServer and the in-VM Caddy reverse-proxy. The `direct:` flag doesn't control whether the port is exposed (every service with a `hostname` + `port` is exposed on `127.42.0.N:port` via softnet); it controls whether the service is HTTP-fronted or raw-TCP end-to-end. Use it for non-HTTP protocols (Postgres, gRPC, custom TCP) that a reverse proxy can't front.

- DNS answers the service's `hostname` with the project's `127.42.0.N` (same as any other hostname on that project), so `psql -h db.test` from the Mac connects to `127.42.0.N:5432` — no ProxyServer hop, no TLS.
- The Mac opens a TCP listener on `127.42.0.N:<port>` and forwards accepted connections into the VM.
- No in-VM reverse-proxy block for the hostname; the workload speaks raw TCP end-to-end.

Rules:

- `direct: true` requires a `hostname` ending in `.test`.
- Adding or removing `direct` is a **live** change: `devm reconcile` applies it on a running VM.
- Non-direct service with a `hostname` → HTTP-fronted (daemon's ProxyServer → in-VM reverse-proxy → your service). Direct service → raw TCP to the same `127.42.0.N`, different port.

---

## Clearing routes

`devm teardown` removes this project's routes automatically before stopping and deleting the VM. Routes are per-project — other projects aren't affected.

To switch routing mode without tearing down (e.g., from `vm` to `local`), just re-run `devm route local` — applying new routes replaces the existing set.

---

## Reaching services from the LAN (`expose_host: true`)

Uncommon; use only when a LAN device (phone, tablet, other laptop) needs to hit a dev service by hostname. devm's CA isn't trusted on those devices and installing it there is annoying, so the pattern is: run a LAN-side reverse proxy that already has a trusted cert (Nginx Proxy Manager, Caddy, Traefik, etc.), and have it forward to devm's shared LAN dispatcher on `0.0.0.0:42000`, preserving the `Host` header. devm dispatches by Host to the right service; TLS is the reverse proxy's problem, not devm's.

```yaml
services:
  everstone:
    port: 80
    hostname: everstone.buzztrack.test
    expose_host: true    # reachable via <mac-lan-ip>:42000
```

- Requires `hostname` (dispatch is Host-header only). Non-direct services only.
- Listener is shared across projects and bound lazily — no `expose_host` opt-in anywhere means no listener. Two projects both claiming the same hostname is rejected at `route apply`.
- `devm status` shows the listener's bound state and hostname count.

---

## Inside the VM: reaching your own services

`*.test` hostnames resolve locally inside the VM to a reverse-proxy that dispatches to `localhost:<port>` for each service you declared. A workload inside the VM that curls `http://api.test/` never leaves the VM — DNS answers loopback, the in-VM proxy dispatches to your service on its declared port.

Under enforced egress, outbound traffic to external destinations is restricted: only HTTPS (:443), HTTP (:80), and NTP (:123) leave the VM. Everything else (arbitrary TCP ports, other UDP) is dropped. HTTP/HTTPS goes through iron-proxy on the Mac and hits the `network.allow` check. During the provisioning window (first boot / `startup:` / template installs), egress is open so `apt-get install` and `curl … | bash` work.

---

## The devm CA

The devm CA is a self-signed root generated once at first daemon start and trusted in the macOS System Keychain (via `devm install`) and inside the VM at first boot. This makes HTTPS to `*.test` names trust-chain-clean in browsers, `curl`, language runtimes, etc. — no cert warnings.

The daemon's ProxyServer signs a leaf cert on demand for whatever SNI the client sends (90-day validity, cached, auto-renewed) using the CA's private key.

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
- `devm skills get secrets` — declaring secrets and passing them through iron-proxy.
