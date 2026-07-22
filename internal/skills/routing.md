---
name: routing
description: devm routing — making *.test domains reach your VM's services (or your Mac's) for development. Covers `devm route local`/`vm`, direct services, and the devm CA.
---

# devm routing reference

## The two destinations

`devm route local` points iron-proxy at a service running on your Mac (upstream = `localhost:port`). `devm route vm` points it at a service running inside the Tart VM (upstream = the VM's IP + declared port). In both cases the client-facing side is iron-proxy on the Mac at the project's `*.test` hostname; the two commands only change what iron-proxy dials as its upstream.

---

## `devm route local`

Run this from the project directory when your dev server runs on the Mac itself:

```
devm route local
```

`devm` reads `devm.yaml`, collects every service that declares both `hostname` and `port`, and sends the routes to the daemon. The daemon's in-memory route table is updated immediately — no restart needed.

Subsequent HTTPS requests to `https://api.test` on the Mac hit iron-proxy, which looks up `api.test` and reverse-proxies to `localhost:3000` (or whatever port you declared).

### How `*.test` reaches iron-proxy on the Mac

`devm install` writes `/etc/resolver/test` so macOS's system resolver forwards every `*.test` DNS query to the devm daemon's DNS server. Each running project is allocated its own address from the `127.42.0.1..20` loopback pool; the daemon answers that project's `*.test` A queries with its own `127.42.0.N`, so two projects that both expose `db.test` on 5432 don't collide — each project's `db.test` resolves to a different IP. Iron-proxy on the Mac binds each project's `:80`/`:443` on its own address and routes by `Host:` header, terminating TLS with the devm CA (see [The devm CA](#the-devm-ca) below).

A query for an unknown hostname, or for a project that isn't currently running, gets NXDOMAIN.

---

## `devm route vm`

Run this from the project directory when your service runs inside the VM:

```
devm route vm
```

`devm` looks up the VM's IP address and sends routes to the daemon with the VM's IP + your declared service port as the upstream. iron-proxy now dials the VM directly for that hostname.

### Auto-routing on `devm shell`

`devm shell` automatically applies vm-mode routes when the project has no routes registered yet (best-effort, silent if the daemon is down). If you have already run `devm route local`, that routing is preserved across stop/start cycles and `devm shell` does not overwrite it.

---

## Direct services (`direct: true`)

A service with `direct: true` is reached **directly on the project's `127.42.0.N`**, bypassing iron-proxy and the in-VM reverse-proxy. Use it for raw-TCP / non-HTTP services (e.g. Postgres) that an HTTP reverse proxy can't front.

- DNS answers the service's `hostname` with the project's `127.42.0.N` (same as any other hostname on that project), so `psql -h db.test` from the Mac connects to `127.42.0.N:5432` — no iron-proxy hop, no TLS.
- The Mac opens a TCP listener on `127.42.0.N:<port>` and forwards accepted connections into the VM.
- No in-VM reverse-proxy block for the hostname; the workload speaks raw TCP end-to-end.

Rules:

- `direct: true` requires a `hostname` ending in `.test`.
- Adding or removing `direct` is a **live** change: `devm reconcile` applies it on a running VM.
- Non-direct service with a `hostname` → HTTP-fronted (iron-proxy → in-VM reverse-proxy → your service). Direct service → raw TCP to the same `127.42.0.N`, different port.

---

## Clearing routes

`devm teardown` removes this project's routes automatically before stopping and deleting the VM. Routes are per-project — other projects aren't affected.

To switch routing mode without tearing down (e.g., from `vm` to `local`), just re-run `devm route local` — applying new routes replaces the existing set.

---

## Inside the VM: reaching your own services

`*.test` hostnames resolve locally inside the VM to a reverse-proxy that dispatches to `localhost:<port>` for each service you declared. A workload inside the VM that curls `http://api.test/` never leaves the VM — DNS answers loopback, the in-VM proxy dispatches to your service on its declared port.

Under enforced egress, outbound traffic to external destinations is restricted: only HTTPS (:443), HTTP (:80), and NTP (:123) leave the VM. Everything else (arbitrary TCP ports, other UDP) is dropped. HTTP/HTTPS goes through iron-proxy on the Mac and hits the `network.allow` check. During the provisioning window (first boot / `startup:` / template installs), egress is open so `apt-get install` and `curl … | bash` work.

---

## The devm CA

The devm CA is a self-signed root generated once at first daemon start and trusted in the macOS System Keychain (via `devm install`) and inside the VM at first boot. This makes HTTPS to `*.test` names trust-chain-clean in browsers, `curl`, language runtimes, etc. — no cert warnings.

iron-proxy signs a leaf cert on demand for whatever SNI the client sends (90-day validity, cached, auto-renewed) using the CA's private key.

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
