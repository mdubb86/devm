# Handoff: bisect devm vs probe — the test_07 publish phantom

## Read first

1. `docs/sbx-quirks.md` — section "## 6. devm port-reconcile destabilizes the sandbox endpoint (OPEN)". That's the full statement of the bug.
2. `e2e/probes/probe-publish/main.go` — the **working** Go binary.
3. `internal/orchestrator/shell.go` — devm's **failing** RunShell.
4. The `strip-devm-publish` branch — most recent commit ("WIP: strip-devm-publish bisection branch") documents every strip I tried and what didn't fix it.

## The goal

Find the actual difference between `probe-publish` (Go binary, publishes a stable port) and `RunShell` (Go function, publishes a port that vanishes within ~1.5s and never recovers). Both run under pexpect. Both spawn `nohup sbx run` as an anchor. Both publish via `exec.Command("sbx", "ports", ...)`. The port behaves differently.

**Once we find it, the fix is probably one-line in `RunShell`.** The bisection is the work.

## What's already known (don't re-prove)

These all PASS in the probe and don't reproduce the failure:

- `--nohup` or plain `sbx run` anchor
- pre-publish `sbx ports --json` (currentMappings)
- single publish vs tight-poll verify + 500ms hold + reverify
- snapshot-style `sbx exec NAME sh -c "echo b64 | base64 -d > FILE"`
- user-shell spawn (`sbx exec -it NAME bash`) with stdin=nil OR stdin=os.Stdin
- chdir to workspace dir before publish
- pexpect cwd=workspace
- kit nested inside workspace dir (devm's structure)
- anchor stderr=os.Stderr (PTY when under pexpect)
- 30+ second observation window

These were stripped from devm on the `strip-devm-publish` branch — none fixed it:

- `lock.Acquire` + lock release
- `runDone` goroutine (the `go func() { runCmd.Wait(); runDone <- err }()`)
- `render.WriteDevmDir` (test pre-renders via `devm reconcile --yes`)
- `ReconcilePortsWithRunner` → replaced with single `exec.Command(...).CombinedOutput()` direct publish
- `WriteSnapshot`

After stripping all of the above, devm STILL fails 5/5.

## What's NOT yet tested

These are the remaining structural differences between the stripped `RunShell` and the probe:

1. **applyDefaults** sets `WaitForRunning` and `PollInterval` on `ShellDeps`. Trivial but unverified.
2. **`sandbox.Sandbox` struct** — `RunShell` does `sb := &sandbox.Sandbox{...}` and uses `sb.IsRunning()`, `sb.Exists()`, `sb.Sessions()`. Probe makes raw `exec.Command("sbx", "ls").Output()` calls.
3. **`waitForRunning` helper** vs probe's inline `waitFor()`. Same 250ms polling cadence but different function. May call out to `sb.IsRunning()` which parses sbx output differently.
4. **`waitForExecReady` helper** vs probe's inline `waitFor()`. Same pattern.
5. **`userCmd.Wait()`** blocking — devm blocks on user-shell exit; probe runs an observation loop in parallel.
6. **`ExecSpawner.Start()`** — the indirection layer. Internally just does `exec.Command(...).Start()`. Probably equivalent but worth confirming.
7. **Go runtime / signal mask** — Go sets up default signal handlers for SIGCHLD, SIGURG, etc. When invoked through pexpect, does Go's signal handling differ between probe and devm? Probably not — both are `package main`, both call `os/exec`, but worth ruling out.
8. **File descriptors inherited by sbx subprocess** — Go's `os.OpenFile` defaults to `O_CLOEXEC`, BUT any fd Go's runtime opens internally (e.g. epoll, netpoll) might not. `lsof` on the spawned `sbx` process during the test would show this.
9. **The fact that devm itself was spawned via Go-CLI-wrappers (cobra)** — `cmd/devm/main.go` uses cobra. Probe is a plain `main`. Cobra adds signal handling and an init sequence. Could it set up something that leaks?

## Suggested bisection approach

The cleanest next move is **direction (8)**: capture `lsof -p PID` on the `sbx run` anchor in both the failing devm flow and the passing probe flow. Compare the open file descriptors. Any difference is the lead.

Concretely:

```bash
# In one terminal:
DEVM_DEBUG=shell just e2e-one test_invariant_happy_path
# When the trace says "cold-start: anchor spawned pid=X", quickly:
lsof -p X > /tmp/devm-anchor-fds.txt
```

Then same for the probe:
```bash
# In another terminal:
cd e2e && uv run python -c "
... spawn probe via pexpect with materialize_kit ...
"
# When probe says "running @ ...", lsof -p the anchor's PID.
lsof -p X > /tmp/probe-anchor-fds.txt
diff /tmp/devm-anchor-fds.txt /tmp/probe-anchor-fds.txt
```

If there's a leaked fd in devm's anchor that's not in probe's, that fd's lifecycle may be what destabilizes sbx's port endpoint when it closes.

## Backup approach

If (8) doesn't reveal anything, do the **inverse strip**: take `e2e/probes/probe-publish/main.go` (which works) and add `cobra` cmd-wrapping to it, see if THAT introduces the phantom. If yes, the cobra command framework is the issue. If no, dig further into how `RunShell` is called.

## Constraints

- Pexpect is for driving shell sessions, not for running ad-hoc Python scripts. Inline probes are OK for investigation but useful findings should be elevated to proper sbx tests under `e2e/test_sbx_anchor_*.py` (see `e2e/test_sbx_anchor_12_go_probe_publish.py` for the pattern).
- main has commits that should not be reverted. Only the architectural decision in `docs/sbx-quirks.md` Quirk #6 is open. Everything else is locked in.
- The strip-devm-publish branch is disposable. Feel free to delete it or rebase.
- Don't take destructive git actions without confirmation. Don't push without confirmation.

## Definition of done

- Identify the single behavior in `RunShell` that destabilizes the port endpoint under sbx.
- Add a regression-guard test under `e2e/test_sbx_anchor_*.py` that exhibits the bug *in pure-sbx form* (matching the probe pattern but triggering the phantom). Currently no such test exists — the probe is the negative space (works); we need the smallest positive (fails) variant.
- Fix it in `RunShell`. Run `just e2e` and confirm all green.
- Close Quirk #6 in `docs/sbx-quirks.md`.
