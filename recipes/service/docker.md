---
name: tool/service/docker
category: service
display_name: Docker
description: "Docker Engine + BuildKit inside the sandbox. Built-in via docker true at the top of devm.yaml (no install block required); this recipe covers the intricacies — the two egress paths for run vs build, and how devm's managed buildkitd makes build-time HTTPS transparent."
keywords: docker buildkit dockerfile container build ca certificate mitm iron-proxy
since: recipes-v1.0.0
---

# Docker

> **Note:** unlike most recipes, Docker is *not* installed via a `devm.yaml`
> `install:` block. Set `docker: true` at the top level and devm's built-in
> docker feature handles Engine install, runtime shim, and the docker CLI
> shim. This recipe documents the intricacies of that built-in feature —
> the two egress paths, and how devm makes build-time HTTPS transparent.

## Enable

```yaml
project:
  name: myproj
docker: true
```

That's the whole setup. `devm shell` provisions:

- **Docker Engine** via `get.docker.com`. Socket permissioned so `docker` runs without sudo.
- **`devm-runc-shim`** as the default OCI runtime. Bind-mounts the guest CA into every container so runtime TLS trusts iron-proxy.
- **`devm-docker-shim`** at `/usr/local/bin/docker` shadowing the real `/usr/bin/docker`. Rewrites `docker build …` → `docker buildx build --builder devm …` (unless the user passed an explicit `--builder`) so builds route through devm's managed buildkitd.
- **upstream `buildkitd` v0.28.1** as a socket-activated systemd service, with `[worker.oci] binary = "/usr/local/bin/devm-runc-shim"` in `/etc/buildkit/buildkitd.toml`. Registered with buildx as builder `devm`.
- **Docker Hub allowlist** — `registry-1.docker.io`, `auth.docker.io`, `production.cloudfront.docker.com` added implicitly to iron-proxy's allowlist. Users don't list them under `network.allow`.

Any other registries or hosts you pull/push to still need to be added to `network.allow`.

## Runtime CA env vars (auto-injected into every container)

On top of the CA bind-mount, `devm-runc-shim` injects a fixed set of CA-trust env vars by mutating the OCI spec's `process.env` before the container starts. This applies to EVERY container start — `docker run`, `docker create`, `docker exec` — and to every `RUN` step in `docker build` (since those steps run under the same shim via the devm buildx builder's OCI worker). This covers libraries that ignore the system store and check a specific env var instead. Current list (single source: `internal/caenv/vars.go`):

| Env var | Consumer |
|---|---|
| `SSL_CERT_FILE`, `SSL_CERT_DIR` | Python `ssl`, curl (via OpenSSL) |
| `REQUESTS_CA_BUNDLE` | Python `requests`, urllib3 |
| `CURL_CA_BUNDLE` | curl (direct) |
| `AWS_CA_BUNDLE` | boto3, aws-cli |
| `NODE_EXTRA_CA_CERTS` | Node.js, Bun |
| `UV_SYSTEM_CERTS=1` | uv (opt-in to system store) |
| `HTTPLIB2_CA_CERTS` | httplib2 → google-api-python-client transport |
| `GRPC_DEFAULT_SSL_ROOTS_FILE_PATH` | gRPC (all languages) |
| `GIT_SSL_CAINFO` | git over HTTPS |
| `CARGO_HTTP_CAINFO` | cargo registry + crate fetches |
| `PIP_CERT` | pip |
| `NO_PROXY=*` | disable HTTP proxy vars (iron-proxy is transparent) |

**User overrides win.** If your `docker run` (or the Dockerfile's `ENV`, or a buildkitd config) already sets one of these, the runc-shim leaves it as-is.

## Libraries that hardcode certifi (Python-only edge case)

A handful of Python libraries hardcode `certifi.where()` and ignore every env var above. Trafilatura is the canonical example; the google-api-python-client stack ONLY fails this way if httplib2 is bypassed (httplib2 itself honors `HTTPLIB2_CA_CERTS`).

There is no env-var fix — this is an [explicit certifi maintainer decision](https://github.com/certifi/python-certifi/issues/200). The drop-in workaround is `pip-system-certs`:

```
pip install pip-system-certs
```

Its `.pth` file monkey-patches `ssl` at Python startup to use the system trust store (via [truststore](https://truststore.readthedocs.io/)), which teaches EVERY certifi-hardcoding library on that Python to trust devm's CA — no per-app code changes. Install it once in your dev image (guard it to dev only if the same image runs in prod, where you don't want the monkey-patch).

## Two egress paths (both transparent)

**`docker run` — SNI passthrough via bridge.**
Container traffic exits through the standard bridge. Iron-proxy sees SNI, decides allow/deny, and if allowed passes the TCP connection through unchanged. The container sees the real upstream cert. `devm-runc-shim` bind-mounts the CA anyway so behavior is consistent when iron-proxy DOES rewrite (e.g. header substitution).

**`docker build` — devm-managed buildkitd, MITM.**
The docker-shim rewrites `docker build …` to `docker buildx build --builder devm …`. That builder is a devm-managed buildkitd (upstream v0.28.1, systemd-managed) whose OCI worker is `/usr/local/bin/devm-runc-shim` — so every RUN-step container is prepared by the same shim that runs for `docker run`: CA bind-mount, caenv env-var injection. Zero Dockerfile changes required.

## Common failures

- **`docker run` fails with a cert error.** The runc-shim's bind-mount didn't land. Check `docker inspect <container>` — should show `/etc/ssl/certs/ca-certificates.crt` mounted from the host bundle. If missing, `devm reconcile` to reprovision the runtime config.
- **Iron-proxy denies the pull with 403.** The registry host isn't in `network.allow` and isn't one of the auto-allowed Docker Hub hosts. Add it under `network.allow`.
- **Every runtime `apt-get update` fails** with
  `Err https://download.docker.com/... 403 Forbidden` +
  `E: The repository ... is no longer signed.` — Engine install leaves
  `/etc/apt/sources.list.d/docker.list` enabled, and its host is only reachable
  during the open provision window (the auto-allowlist covers the Docker Hub
  *image* hosts, not the apt repo). One failing repo makes `apt-get update`
  exit non-zero, so anything that apt-installs at runtime breaks — e.g.
  `playwright install --with-deps`. Preferred fix: install apt packages via
  `packages:` (provision-time, open egress) instead of at runtime. If you
  genuinely need runtime apt, allowlist the repo host:

  ```yaml
  network:
    allow:
      - download.docker.com   # Engine's apt repo; only for runtime apt-get
  ```

## Debugging build-time trust

If `docker build` fails with "buildx builder \"devm\" not found or unhealthy" the shim's runtime check tripped. Check the builder + service:

- `docker buildx inspect devm` — builder registration state.
- `sudo systemctl status buildkit.socket buildkit.service` — buildkitd health.
- `sudo cat /etc/buildkit/buildkitd.toml` — must contain `[worker.oci] binary = "/usr/local/bin/devm-runc-shim"`.

`devm reconcile` reruns the install script's idempotent steps and re-registers the builder if missing.

## Not covered here

- **BuildKit cache mounts** (`RUN --mount=type=cache`) — orthogonal to the CA question, use as you normally would.
- **Multi-stage builds** — no special handling needed; every stage's RUN steps run under the same devm-managed buildkitd, so CA trust and caenv vars apply uniformly.
- **Rootless docker** — devm's docker runs as root inside the guest by design. Rootless is out of scope.
