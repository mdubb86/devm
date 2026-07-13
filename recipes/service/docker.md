---
name: tool/service/docker
category: service
display_name: Docker
description: "Docker Engine + BuildKit inside the sandbox. Built-in via docker true at the top of devm.yaml (no install block required); this recipe covers the intricacies — the two egress paths for run vs build, the Dockerfile RUN block needed for build-time HTTPS, and portability across Mac and CI."
keywords: docker buildkit dockerfile container build ca certificate mitm iron-proxy
since: recipes-v1.0.0
---

# Docker

> **Note:** unlike most recipes, Docker is *not* installed via a `devm.yaml`
> `install:` block. Set `docker: true` at the top level and devm's built-in
> docker feature handles Engine install, runtime shim, and the docker CLI
> shim. This recipe documents the intricacies of that built-in feature —
> the Dockerfile pattern users need to add, and why.

## Enable

```yaml
project:
  id: myproj
  vm_name: myproj-vm
docker: true
```

That's the whole setup. `devm shell` provisions:

- **Docker Engine** via `get.docker.com`. Socket permissioned so `docker` runs without sudo.
- **`devm-runc-shim`** as the default OCI runtime. Bind-mounts the guest CA into every container so runtime TLS trusts iron-proxy.
- **`devm-docker-shim`** at `/usr/local/bin/docker` shadowing the real `/usr/bin/docker`. Auto-injects `--secret id=devm-ca,src=/usr/local/share/ca-certificates/devm.crt` on `docker build` and `docker buildx build`.
- **Docker Hub allowlist** — `registry-1.docker.io`, `auth.docker.io`, `production.cloudfront.docker.com` added implicitly to iron-proxy's allowlist. Users don't list them under `network.allow`.

Any other registries or hosts you pull/push to still need to be added to `network.allow`.

## Two egress paths (why runtime "just works" and builds don't)

Understanding these matters when debugging cert-verify failures:

**`docker run` — SNI passthrough via bridge.**
Container traffic exits through the standard bridge. Iron-proxy sees SNI, decides allow/deny, and if allowed passes the TCP connection through unchanged. The container sees the real upstream cert. No CA needed — but `devm-runc-shim` bind-mounts one anyway so behavior is consistent when iron-proxy DOES rewrite (e.g. header substitution).

**`docker build` — BuildKit sandbox, MITM.**
BuildKit runs each RUN step in its own sandbox with a different network path that falls through to iron-proxy's MITM path. Container gets iron-proxy's re-signed cert. Without devm's CA in the build container, cert-verify fails.

BuildKit has no first-class "trust this CA globally" knob (confirmed against upstream — every trust surface, `buildkitd.toml`/`daemon.json`/`certs.d`, covers image pulls only). The fix is a per-RUN secret mount, which the docker shim makes transparent.

## The Dockerfile pattern (required for build-time HTTPS)

Any Dockerfile that hits HTTPS inside a RUN step — `apt-get update`, `curl`, `npm install`, `pip install`, `git clone` — needs this block near the top, before any HTTPS-using RUN:

```dockerfile
# syntax=docker/dockerfile:1
FROM debian:bookworm-slim  # or alpine:3.22, ubuntu:latest, fedora, rhel — same block works

RUN --mount=type=secret,id=devm-ca,dst=/usr/local/share/ca-certificates/devm.crt,required=false \
    [ -s /usr/local/share/ca-certificates/devm.crt ] && \
    ( command -v update-ca-certificates >/dev/null 2>&1 && update-ca-certificates \
      || cat /usr/local/share/ca-certificates/devm.crt >> /etc/ssl/certs/ca-certificates.crt ) \
    || true

# your normal build steps below — HTTPS inside RUN now works
RUN apt-get update && apt-get install -y curl && curl https://api.example.com/...
```

**What each part does:**

- `# syntax=docker/dockerfile:1` — opts into the frontend that supports `--mount=type=secret`.
- `--mount=type=secret,id=devm-ca` — buildkit binds the secret (delivered by the shim as `--secret id=devm-ca,src=…`) at `dst` for the duration of this RUN only. Not persisted to any image layer.
- `dst=/usr/local/share/ca-certificates/devm.crt` — Debian's canonical CA drop-in dir. Chosen so that when `update-ca-certificates` IS available, it finds and merges the cert automatically.
- `required=false` — critical for portability. If the secret isn't defined (no shim in the path), the mount ends up as an empty file rather than failing the build.
- `[ -s … ]` — "file exists AND is non-empty." Anything to do at all? On environments without the shim (Mac, CI), the mount is empty; the test fails; the `||` short-circuits the whole tail and the RUN exits 0.
- `command -v update-ca-certificates` — is Debian's canonical tool available? Debian/Ubuntu/Fedora with `ca-certificates` installed: yes → use it (survives later CA-bundle rebuilds by the same tool). Alpine bare / minimal images: no → fall through.
- `cat … >> /etc/ssl/certs/ca-certificates.crt` — universal bundle append. Works on any distro that ships the standard bundle path (Alpine, Debian, Ubuntu, Fedora, RHEL, most base images).
- `|| true` — normalizes exit code so the RUN doesn't fail the build on the no-op path.

## Portability: same Dockerfile everywhere

The `required=false` + guard-then-skip pattern is deliberate. The same Dockerfile builds cleanly in three environments:

| Environment | Shim present | `--secret` passed | RUN behavior |
|---|---|---|---|
| Inside `devm shell` | yes | yes (auto) | mount populated → update-ca-certificates OR bundle-append runs → CA merged |
| Plain `docker build` on Mac | no | no | mount empty → guard false → RUN no-ops |
| CI (plain `docker build`) | no | no | same as Mac |

No if/else, no environment detection. Iron-proxy isn't in the network path on Mac or CI, so no CA is needed there — the RUN step correctly does nothing.

## Common failures

- **`docker build` fails with `stat /usr/local/share/ca-certificates/devm.crt: no such file or directory`.** The shim injected the flag, but the CA file is missing from the guest. Usually means the sandbox was provisioned before the CA install ran successfully — `devm reconcile` or a fresh `devm teardown && devm shell` fixes it.
- **`docker build` fails with `x509: certificate signed by unknown authority` inside a RUN step.** The Dockerfile is missing the RUN block above (or the block is *after* the HTTPS-using RUN). Add it near the top of the Dockerfile.
- **`docker run` fails with the same cert error.** The runc-shim's bind-mount didn't land. Check `docker inspect <container>` — should show `/etc/ssl/certs/ca-certificates.crt` mounted from the host bundle. If missing, `devm reconcile` to reprovision the runtime config.
- **Iron-proxy denies the pull with 403.** The registry host isn't in `network.allow` and isn't one of the auto-allowed Docker Hub hosts. Add it under `network.allow`.

## Not covered here

- **BuildKit cache mounts** (`RUN --mount=type=cache`) — orthogonal to the CA question, use as you normally would.
- **Multi-stage builds** — the CA install RUN needs to appear in every stage that hits HTTPS.
- **Rootless docker** — devm's docker runs as root inside the guest by design. Rootless is out of scope.
