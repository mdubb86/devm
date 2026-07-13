# devm

A Mac+VM dev sandbox tool. Single Go binary + Claude Code plugin.

## Install

```bash
# Homebrew (recommended for Mac):
brew install mdubb86/tap/devm

# Curl one-liner:
curl -fsSL https://raw.githubusercontent.com/mdubb86/devm/main/scripts/install.sh | bash
```

Both paths drop devm on your PATH and ship the same binary
(darwin/arm64 + darwin/amd64). To upgrade later:

```bash
brew upgrade mdubb86/tap/devm   # if you installed via brew
devm upgrade                    # if you installed via curl or manually
```

`devm version` prints the installed version + commit + build date.

### Wire into Claude Code

Once devm is on your PATH, install the Claude Code skill stubs:

```bash
npx skills add mdubb86/devm -g --agent claude-code
```

> Note the argument order: `skills add` wants the source *before* the
> flags. Putting the flags first errors with "Missing required argument:
> source".

That drops two skills under `~/.claude/skills/`: a small discovery
stub (`devm`) and a reference card (`using-devm`). Claude Code
auto-activates them when working with `devm.yaml`, then the stub
calls `devm skills list` / `devm skills get <name>` to fetch the
workflow content from this binary (so it stays version-locked).

For project-local install drop `-g`; for other agents swap
`--agent claude-code` for `--agent '*'` (or your agent of choice).
The `--agent claude-code` flag is the critical bit — without it the
installer drops to `.agents/skills/…` and Claude Code won't see it.

## Quickstart

```bash
cd ~/your-project
devm validate     # check devm.yaml
devm reconcile    # regenerate .devm/ kit assets
devm shell        # drop into the sandbox
devm version      # current version + build info
devm upgrade      # self-update (no-op for brew installs)
```

## devm.yaml affordances

A few things devm does so your `devm.yaml` doesn't have to:

* **`apt-get update` already ran.** Devm's bootstrap step runs `apt-get update`
  before any of your `install:` entries, so they can `apt-get install -y <pkg>`
  directly.
* **Failures surface with captured output.** Each `install:` and `startup:` step
  is wrapped: stdout+stderr is captured, exit codes are tracked, and `devm shell`
  prints a structured error showing which step failed and what it printed.
  Logs persist at `/tmp/.devm-install/install-<N>/current` and
  `/tmp/.devm-startup/startup-<N>/current` inside the sandbox.
* **The `ncurses-term` package is installed** (modern terminfo for TUIs).
  Devm also embeds and drops a static `s6-log` binary at `.devm/scripts/s6-log`
  for `wrap-bg.sh` to use without any apt step.

## Docker builds and iron-proxy

When you enable `docker: true` in `devm.yaml`, devm installs a Docker CLI
shim (`/usr/local/bin/docker`) that shadows the real docker at
`/usr/bin/docker`. Its only job: append `--secret id=devm-ca,src=/etc/ssl/certs/devm.crt`
on `docker build` and `docker buildx build`. Every other subcommand
passes through unchanged.

The shim exists because BuildKit's build sandbox goes through
iron-proxy's MITM path (unlike plain `docker run`, which uses SNI
passthrough on the bridge). Without the CA, HTTPS calls inside a RUN
step fail cert-verify.

To take advantage, add one RUN block near the top of any Dockerfile
that does HTTPS in build steps:

```dockerfile
# syntax=docker/dockerfile:1
FROM debian:bookworm-slim   # or alpine:3.22, ubuntu:latest, fedora — same block works

RUN --mount=type=secret,id=devm-ca,dst=/usr/local/share/ca-certificates/devm.crt,required=false \
    [ -s /usr/local/share/ca-certificates/devm.crt ] && \
    ( command -v update-ca-certificates >/dev/null 2>&1 && update-ca-certificates \
      || cat /usr/local/share/ca-certificates/devm.crt >> /etc/ssl/certs/ca-certificates.crt ) \
    || true

# your normal build steps below — HTTPS inside RUN now works
RUN apt-get update && apt-get install -y curl && curl https://…
```

The block uses `update-ca-certificates` when the base image ships it
(Debian family), and falls back to appending the CA to the bundle at
`/etc/ssl/certs/ca-certificates.crt` when it doesn't (Alpine and other
minimal images). No distro branching in your Dockerfile.

**Portability.** The `required=false` on the mount makes the RUN a
no-op wherever the secret isn't defined — the same Dockerfile builds
cleanly on your Mac (`docker build .`), in CI, and inside the devm
sandbox. Only the sandbox path installs the CA; everywhere else the
mount is empty, the `[ -s ... ]` test is false, and the RUN skips
without failing the build.

(More docs as the project matures.)
