---
name: routing
description: devm routing — making *.test domains reach your VM's services (or your Mac's) for development. Covers the per-project loopback IPs, iron-proxy, softnet, the in-VM Caddy + dnsmasq, and the devm CA.
---

# devm routing reference

## The two destinations

`devm route local` points the daemon proxy at a service running on your Mac (the daemon proxies to `localhost:port`). `devm route vm` points the daemon proxy at a service running inside the Tart VM. In both cases requests enter through iron-proxy on the Mac at the project's own `127.42.0.N` loopback address; the two commands only change what iron-proxy dials as its upstream.

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

Subsequent HTTPS requests to `https://api.test` on the Mac hit the project's iron-proxy at `127.42.0.N:443`, which looks up `api.test` in the route table and reverse-proxies to `localhost:3000` (or whatever port you declared).

### How `*.test` reaches iron-proxy on the Mac

`devm install` writes `/etc/resolver/test` with:

```
nameserver 127.0.0.1
port 51153
```

macOS's system resolver reads this file and forwards every `*.test` DNS query to the daemon's DNS server at `127.0.0.1:51153`. The daemon DNS server doesn't answer every project with the same loopback address: each running project is allocated its own address from a pool of `127.42.0.1`..`127.42.0.20`, and the DNS server answers that project's `*.test` A queries with its allocated `127.42.0.N`. This is what lets two projects each run a service on the same port (say, both expose `db.test` on 5432) without colliding — every project's `db.test` resolves to a different IP. A query for an unknown hostname, or for a project that isn't currently running, gets NXDOMAIN rather than an address.

iron-proxy binds `:80` and `:443` on each running project's own `127.42.0.N`. The unprivileged daemon can't bind low ports itself; the companion root helper (`com.devm.helper`) pre-binds each pool address's :80 and :443 and hands the file descriptors back over its Unix socket via `SCM_RIGHTS`. Once the FDs are handed off, iron-proxy accepts on them like any other listener. It routes by the `Host:` request header and terminates TLS with the devm CA (see [The devm CA](#the-devm-ca) below).

---

## `devm route vm`

Run this from the project directory when your service runs inside the VM:

```
devm route vm
```

What happens:

1. `devm` queries Tart for the VM's current IP address (using `cfg.Project.Name`). The VM must be running; start it first with `devm shell` if needed.
2. It sends `POST /routes/apply` to the daemon with `BackendHost=VM_IP` and `BackendPort=svc.Port` for each hostname.
3. iron-proxy now dials `VM_IP:svc.Port` directly when a request arrives for that hostname.

The daemon does not use the in-VM Caddy for this path — it connects to the service's declared port on the VM's IP directly.

### Auto-routing on `devm shell`

`devm shell` automatically applies vm-mode routes when the project has no routes registered yet (best-effort, silent if the daemon is down). If you have already run `devm route local`, that routing is preserved across stop/start cycles and `devm shell` does not overwrite it.

---

## Direct services (`direct: true`)

A service with `direct: true` is reached **directly on the project's `127.42.0.N`**, bypassing iron-proxy and the in-VM Caddy. Use it for raw-TCP / non-HTTP services (e.g. Postgres) that an HTTP reverse proxy can't front.

What happens for a direct service:

1. **DNS answers the project IP.** The daemon's `*.test` resolver answers the service's `hostname` with the project's allocated `127.42.0.N` (same as any other hostname on that project). So `psql -h db.test` from the Mac connects to `127.42.0.N:5432` — no iron-proxy hop, no TLS.
2. **softnet binds the port on the Mac.** softnet's ingress opens a TCP listener on `127.42.0.N:<port>` and forwards accepted connections into the guest's netstack toward the same port inside the VM. For ports <1024, softnet routes the bind through the root helper (same `SCM_RIGHTS` handoff iron-proxy uses); ports ≥1024 bind directly.
3. **No Caddy.** The in-VM Caddyfile gets no block for the hostname; the workload speaks raw TCP end-to-end.

Rules and lifecycle:

- `direct: true` requires a `hostname` ending in `.test`.
- It is a **live** change: adding or removing `direct` takes effect on a running VM via `devm reconcile` (softnet's expose set is re-pushed and the Caddyfile is re-rendered), and it persists across a restart.
- Contrast with the proxied path above: a non-direct service with a `hostname` is HTTP-fronted (iron-proxy on `127.42.0.N:80/:443` → in-VM Caddy → service); a direct service is raw TCP to the same `127.42.0.N`, different port.

---

## Clearing routes

Routes for a project are cleared when you run:

```
devm teardown
```

`teardown` sends `POST /routes/remove` to the daemon before stopping and deleting the VM. The route table is per-project: routes for other projects are not affected.

If you want to switch routing mode without tearing down (e.g., from `vm` to `local`), just re-run `devm route local` — applying new routes replaces the existing set for that project.

---

## Inside the VM: where does traffic go?

The VM has no direct network to your LAN. `tart run --net-softnet` terminates the guest's virtio NIC into softnet — a userspace network stack on the Mac (gvisor-tap-vsock) — over a vsock channel. Every guest packet lands in softnet, which decides its fate per the live egress policy the daemon has flipped it to (LOCKED at boot, OPEN during provisioning, ENFORCED once services come up).

Under ENFORCED policy:

- **TCP :80 and :443** — forwarded to that project's iron-proxy listeners on the Mac (`127.42.0.N:80`, `127.42.0.N:443`). iron-proxy applies `network.allow` and MITMs TLS with the devm CA.
- **All other TCP** — dropped (RST). The workload sees a connection refused.
- **UDP :123 (NTP)** — forwarded to devm's SNTP responder on the Mac, so `systemd-timesyncd` in the guest resyncs from the Mac's clock after a Mac sleep. The base image is preconfigured to point NTP at `192.0.2.1` (RFC 5737 documentation address) as a stable target; softnet intercepts by dest port, not IP.
- **DNS (:53)** — handled locally by softnet's own resolver on its gateway address; queries for `devm.test.` are answered directly, and everything else is proxied to iron-proxy's DNS which returns iron-proxy-safe answers.

Under OPEN policy (the provisioning window), TCP and UDP forward to their original destinations and DNS goes to the Mac's real resolver — that's what lets `apt-get install` and `curl … | bash` run at first boot.

Inside the guest itself, the workload also gets an in-VM name-resolution + reverse-proxy path for `*.test` hostnames that lets services reach each other without going out through softnet at all:

1. **dnsmasq** runs at `127.0.0.1:53` inside the VM with a single drop-in: `address=/test/127.0.0.1`. Any `*.test` query is answered locally with `127.0.0.1`. Everything else forwards upstream (to softnet's gateway resolver).

2. **Caddy** runs at `127.0.0.1:80` inside the VM with a generated `/etc/caddy/Caddyfile`: one `http://hostname { reverse_proxy localhost:port }` block per service that declares a hostname. `auto_https off` — TLS for cross-VM/Mac access is iron-proxy's job; in-VM traffic is plain HTTP over loopback. Reloaded on `devm reconcile` when hostnames or ports change.

So a workload inside the VM that curls `http://api.test/` resolves `api.test` → `127.0.0.1` via dnsmasq, hits Caddy, and Caddy dispatches to `localhost:<port>` — never leaving the VM. A workload that curls `https://api.example.com/` (an external destination) goes out over vsock to softnet, which under ENFORCED policy sends it to iron-proxy, which allow-list-checks and MITMs.

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

iron-proxy uses the CA's `GetCertificate` callback: leaf certs are signed on demand for whatever SNI the client sends (90-day validity, cached in memory, renewed automatically when fewer than 7 days remain before expiry).

At first boot, the provisioner also installs the CA root inside the VM:

```
/usr/local/share/ca-certificates/devm.crt
```

followed by `update-ca-certificates`, so tools running in the VM (curl, git, language runtimes) trust the CA for any HTTPS to `*.test` names that flow back through iron-proxy.

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
