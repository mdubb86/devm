---
name: scripting
description: Author project scripts in devm.sh / devm.me.sh. Same functions run on both sides — in the guest via `gdevm run <fn>`, on the Mac via `devm run <fn>` — with $DEVM_SIDE telling the script which side it's on.
---

# Scripting with devm

Project tooling lives in two files at the project root:

| File | Checked in? | Scope |
|---|---|---|
| `devm.sh` | Yes | shared across everyone working on the project |
| `devm.me.sh` | No (gitignored) | personal, per-developer |

Both files are plain bash. Any top-level function defined in either file
is runnable:

- In the guest: `gdevm run <fn> [args]` — the standard guest-side
  runner. Also sourced automatically by `install()` and `startup()` at
  provisioning, and by any `services.<name>.exec: <fn>` scalar
  reference.
- On the Mac: `devm run <fn> [args]` — the host-side runner.

`devm.me.sh` is merged into the same function set as `devm.sh`. A name
defined in both files is a validation error (ambiguous).

## The side marker

The wrapper exports `DEVM_SIDE` before invoking the function:

- `DEVM_SIDE=guest` inside the VM.
- `DEVM_SIDE=host` under `devm run` on the Mac.

Branch on it when a function should behave differently depending on
where it runs:

```bash
open_frontend() {
  local url="https://app.${PROJECT}.test"
  if [ "$DEVM_SIDE" = "host" ]; then
    open "$url"             # macOS `open`
  else
    xdg-open "$url"         # guest desktop (rare in devm) or a stub
  fi
}
```

`DEVM_SIDE` is reserved — `devm.yaml`'s `env:` block cannot set it.

## Environment the function sees

- `WORKSPACE` — the project root. Mac-side path on the host, guest-side
  path (usually `/home/devm/<repo-label>` or `/home/devm` for a
  repo-less project) in the guest.
- `DEVM_SIDE` — `host` or `guest`, see above.
- Every entry in `devm.yaml`'s `env:` block that is NOT a secret
  reference. `${WORKSPACE}` substitution has already happened.
- `PATH` is prepended with every entry in `devm.yaml`'s `path:` block.
- Per-service `env:` entries are NOT merged in here — they're applied
  to the service's own process, not to `run` invocations.

Secrets are NEVER passed to scripts on either side. An `env:` entry
referenced as a secret is simply absent from the function's env (bash
treats the variable as empty). If a script needs a secret, it should
fetch it at run-time from wherever secrets live for that project.

## Behavior details

- **Positional args** passed after `<fn>` reach the function as `$1`,
  `$2`, … Flags that start with `-` go to `devm run` / `gdevm run`
  itself; use `--` before the args if you need to pass flag-shaped
  values to the function.
- **`cwd`** inside the function is the project root, regardless of
  which subdirectory you invoked `devm run` from. The walk-up finds
  the project the same way `devm shell` and `devm secret` do.
- **Exit code** of the function becomes the runner's exit code.
  Shell chains like `devm run build && devm run deploy` short-circuit
  the way bash users expect.
- **Signals** (Ctrl-C / SIGTERM) sent to `devm run` are forwarded to
  the bash child, so a long-running function (dev server, build) can
  be interrupted cleanly.
- **No daemon required** for `devm run` — the Mac-side runner loads
  `devm.yaml` directly from disk. It works even before the project's
  VM has ever been created, which is the point: bootstrap scripts
  should be the easy case.
- **VM state doesn't gate `devm run`.** Whether the VM is absent,
  stopped, or running, `devm run` executes. If a function wants to
  probe the VM it does so in its own body.

## Errors

- `no devm.sh in <project>` — the project has no `devm.sh` and no
  `devm.me.sh` defining any function. Add one.
- `function "X" not found; available: a, b, c` — the function name
  doesn't exist. The listing shows what does.
- A syntax error in `devm.sh` surfaces the bash parse error directly
  (not a confusing "function not found").

## What NOT to use

- **`IS_SANDBOX`** is gone. Any legacy `if [ "$IS_SANDBOX" = "1" ]`
  check breaks silently — update to `[ "$DEVM_SIDE" = "guest" ]`.
