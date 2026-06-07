---
name: errors
description: Reference — how to read supervision error blocks and where logs live.
hidden: true
---

# Supervision error patterns

If `devm shell` prints a structured error like:

```
error: install step 3 failed (rc=1)
  command: apt-get install -y nonexistent-pkg
  output (last N bytes of /tmp/.devm-install/install-3/current):
    Reading package lists...
    E: Unable to locate package nonexistent-pkg
```

The error block is everything the agent needs. The `output` section
contains the actual failing command's stdout+stderr, captured by the
wrap-fg.sh wrapper. Don't re-run the failing command speculatively;
read the captured output.

## Patterns

| Pattern | Meaning | Action |
|---|---|---|
| `error: install step N failed (rc=R)` | A user `install:` command exited non-zero. Sbx tore down the sandbox per its contract. | Read `output:` block. Likely fix is in the captured stderr. |
| `error: startup step N failed (rc=R)` | A user `services[*].startup:` command failed. Sandbox is silent on startup failure but devm detected it via marker files. | Read `output:` block. Daemon failures (port-in-use, missing config) common. |
| `error: install did not complete` / `step N still running or hung` | Install gate timed out (default 120s). The hanging step's captured output is in `output:`. | Often an apt or network-blocked install. Check network policy. |
| `error: startup did not complete` / `step N still running or hung` | Startup gate timed out (default 30s). | Often a service that opens a port and hangs but never signals ready. |

## Where logs live (if user wants more than the error block)

Inside the sandbox (use `sbx exec NAME cat ...`):

- `/tmp/.devm-install/install-<N>/current` — captured stdout+stderr per install step
- `/tmp/.devm-install/install-<N>.rc` — exit code
- `/tmp/.devm-install/install-<N>.ok` — present iff step succeeded
- `/tmp/.devm-startup/<...>` — same layout for startup phase

On the host (in case the sandbox is gone after an install failure):

- `<repoRoot>/.devm-failures/install-<N>.current` — mirrored failure record
- `<repoRoot>/.devm-failures/install-<N>.rc` — mirrored exit code

## Gate timeouts (test/debug overrides)

- `DEVM_INSTALL_GATE_TIMEOUT_S=<seconds>` — override the install gate timeout.
- `DEVM_STARTUP_GATE_TIMEOUT_S=<seconds>` — override the startup gate timeout.

Defaults: 120s install, 30s startup.
