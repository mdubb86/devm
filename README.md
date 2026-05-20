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

(More docs as the project matures.)
