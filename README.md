# devm

A Mac+VM dev sandbox tool. Single Go binary + Claude Code plugin.

## Install

```bash
brew install mtwaage/tap/devm
# or
go install github.com/mtwaage/devm/cmd/devm@latest
```

## Quickstart

```bash
cd ~/your-project
devm validate     # check devm.yaml
devm reconcile    # regenerate .devm/ kit assets
devm shell        # drop into the sandbox
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
