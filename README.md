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

(More docs as the project matures.)
