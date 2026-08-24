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
devm reconcile    # apply devm.yaml changes
devm shell        # drop into the sandbox
devm version      # current version + build info
devm upgrade      # self-update (no-op for brew installs)
```

**Your project needs a `repo:` block in `devm.yaml`** — devm hydrates the
sandbox's workspace via `git clone` at first cold-start. Simplest form:

```yaml
repo:
  secret: gh_token        # names a secret in devm's store; iron-proxy substitutes it in the git-clone Authorization header
```

When `url:` is omitted, devm derives it from `git remote get-url origin`
in the project directory. See `devm skills get schema` for the full
shape (multi-repo, per-volume overrides).

## devm.yaml affordances

A few things devm does so your `devm.yaml` doesn't have to:

* **`apt-get update` already ran.** Devm's bootstrap step runs `apt-get update`
  before any of your `install:` entries, so they can `apt-get install -y <pkg>`
  directly.
* **Failures surface with captured output.** Each `install:` and `startup:` step
  is wrapped: stdout+stderr is captured, exit codes are tracked, and `devm shell`
  prints a structured error showing which step failed and what it printed.
* **The `ncurses-term` package is installed** (modern terminfo for TUIs).

## Docker builds and iron-proxy

When you enable `docker: true` in `devm.yaml`, devm installs upstream
`buildkitd` v0.28.1 as a systemd service, points its OCI worker at
`devm-runc-shim`, and registers it with buildx as builder `devm`. The
Docker CLI shim (`/usr/local/bin/docker`, shadowing the real docker at
`/usr/bin/docker`) rewrites `docker build …` to
`docker buildx build --builder devm …`; every other subcommand passes
through unchanged. Because every RUN step's sandbox is then prepared
by `devm-runc-shim` — same as any other container start — build-time
HTTPS is transparent: no CA-install RUN block, no Dockerfile changes
at all.

Runtime containers get the same treatment. `docker run`, `docker create`,
and `docker exec` also route through `devm-runc-shim`, which bind-mounts
`/etc/ssl/certs/ca-certificates.crt` into the container and appends the
caenv CA env vars (`SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, etc.) to the
OCI spec's `process.env`. If your `docker run` invocation or the
Dockerfile's `ENV` already sets one of those vars, your value wins.

See `recipes/service/docker.md` for the full recipe — the two egress
paths, the env-var table, and troubleshooting.
