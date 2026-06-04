# sbx interaction quirks & workarounds

`sbx` works reliably, but a few behaviors around timing and I/O surprised
us during development. Each is handled in code with a workaround; this
doc records the observed behavior, where we work around it, and how to
reproduce — useful for future debugging and for any upstream report.

These are quirks, not bugs we're confident about. Treat sbx as correct
and our orchestration as needing to accommodate its timing.

## 1. `sbx exec` with piped stdin hangs from Go's `exec.Cmd`

**Observed:** `WriteSnapshot` originally ran
`sbx exec <name> sh -c "cat > FILE"` with the snapshot content piped via
`exec.Cmd.Stdin = strings.NewReader(content)`. The call hung
indefinitely — `cmd.Run()` never returned — even though the identical
command in an interactive shell (`echo ... | sbx exec <name> sh -c
"cat > FILE"`) completes in under 2s.

Verified with timing logs: a `WriteSnapshot start` line printed but the
matching `end` never did, in the cold-start path.

**Workaround:** `internal/orchestrator/snapshot.go` — pass the content
base64-encoded on the command line instead of via stdin:
`sbx exec <name> sh -c "... echo <b64> | base64 -d > FILE ..."`. No
stdin pipe, no hang. Snapshots are small (~1KB) so ARG_MAX is irrelevant.

**Not fully understood:** why Go's stdin-pipe goroutine never completes
against `sbx exec` specifically. The base64 path sidesteps it entirely.

**Regression guard:** `e2e/test_sbx_quirk_05_exec_stdin_pipe_hang.py`.

## 2. Port reconcile must happen AFTER the anchor handoff

**Observed:** During cold start, devm needs the `sbx run` anchor
session alive long enough to bring the sandbox up, then hands off to
the user's `sbx exec -it` session before killing the anchor. Empirically,
running `sbx ports --publish` *while the anchor still holds the session*
results in mappings that vanish when the anchor is killed.

**Workaround:** `internal/orchestrator/shell.go` defers port reconcile
until after the anchor is killed and the safety check confirms the
sandbox is still up on the user session.

**Status under the new anchor-alive architecture:** dissolved. The
anchor never dies, so mappings published under its session never
vanish. Pinned positively by
`e2e/test_sbx_anchor_05_publish_sticks.py`.

## 3. First post-handoff `sbx ports --publish` is a phantom

**Observed:** The first `sbx ports --publish` immediately after the
anchor session is killed returns success ("Published 127.0.0.1:X ->
Y/tcp" rc=0) AND the mapping briefly appears in `sbx ports --json` —
but it never durably applies; nothing is listening on the host port.
The SECOND publish is the one that actually sticks.

In bench tests, ~5/6 cold starts hit the phantom on the first publish.
A bare retry loop that trusts the first verify-true silently believes
the publish succeeded and stops — leaving the user with a port that's
listed-then-gone.

**Workaround:** `internal/orchestrator/ports.go` `publishWithVerify` —
after `verifyMappingVisible` returns true, hold 500ms and re-verify.
If the mapping is still there, real success; if gone, loop and
re-publish.

Layered with this is the visibility lag handling: after publish, poll
`sbx ports --json` for up to 3s before re-issuing. Tolerate ONLY the
specific "already published" error:
`publish port: port 127.0.0.1:<host>/tcp is already published`.

The full investigation log is in
[`docs/sbx-port-investigation.md`](sbx-port-investigation.md).

**Status under the new anchor-alive architecture:** dissolved. There
is no post-handoff publish — publish happens any time, in any order,
while the same long-lived anchor session is up.

## 4. `exec.Cmd.Output()` discards the real error message

Not an sbx quirk, but it hid quirks 1-3 during debugging. `cmd.Output()`
returns an `*exec.ExitError` whose `Error()` is just `"exit status N"`;
the actual stderr (e.g. sbx's error text) is in `ExitError.Stderr`.

**Fix:** `internal/sandbox/sbx.go` `DefaultRunner.Output` folds the
stderr text into the returned error so callers see the real message
(and `publishWithVerify` can string-match the "already published" case).

## Reproduction notes

- Quirks 2 & 3 reproduce most reliably with a *minimal* sandbox: one
  service with a `canonical` port and no `install` or `startup`
  commands. Adding a background startup daemon masks them.
- The e2e suite (`just e2e`) exercises the workarounds end-to-end.
  `e2e/test_08_reconcile_live_port.py` is the regression guard for the
  port behaviors; `e2e/test_sbx_*.py` isolate sbx's own semantics.

## 5. Background-startup daemons: kit's `background: true` is wrong, foreground + `nohup ... &` is right

**TL;DR.** To launch a long-running daemon from a kit's
`commands.startup`, DO NOT use the kit's own `background: true` flag.
Use a **foreground** step whose command is wrapped at the SHELL level
with `nohup ... > log 2>&1 &`. The kit flag is a different feature
with its own ~5s lifetime; the shell-`&` pattern is what production
kits (e.g. docker/sbx-kits-contrib/code-server) actually use.

### How we learned

We initially read the docs as saying "`background: true` plus
`nohup ... &` wrapping" was the pattern. Building a regression guard
revealed the kit's `background: true` flag kills the process after
~5 seconds regardless of what's wrapped inside it (now pinned by
`e2e/test_sbx_quirk_04_kit_background_true.py`).

Cross-checking the actual community kits showed the real working
pattern: a plain **foreground** startup step that ends with shell `&`:

```yaml
# Excerpt from docker/sbx-kits-contrib/code-server/spec.yaml:
commands:
  startup:
    - command: ["sh", "-c", "nohup /home/agent/.local/bin/start-code-server.sh > /tmp/code-server.log 2>&1 &"]
      user: "1000"
      description: Start code-server in the background, opened on the primary workspace
```

No `background: true`. The `&` in the shell command does the
backgrounding; the startup step itself returns immediately; nohup
keeps the child alive past the parent shell's exit.

### What devm does

`internal/render/spec.go` `writeStartupStep` translates
`services.<name>.startup[*]` entries with `background: true` in
**devm.yaml** into the community pattern above:

- Wraps the user's `command:` argv with `sh -c 'nohup <quoted argv> > /tmp/devm-startup-<svc>-<idx>.log 2>&1 &'`
- Renders as a foreground sbx kit step (does NOT emit `background: true` into spec.yaml)
- Sets `user: "1000"` and a descriptive label

So users keep writing the simple devm.yaml form
(`startup: [{command: [...], background: true}]`) and devm produces
the correct sbx-kit YAML automatically.

### Regression guards

- `e2e/test_sbx_anchor_01_alive_no_user_shell.py` — anchor alive +
  no user shell, daemon lives >25s
- `e2e/test_sbx_quirk_01_anchor_kill_kills_daemon.py` — anchor
  killed under shared/no-PTY user shell, daemon dies at ~5s
- `e2e/test_sbx_quirk_02_fresh_pty_masks_kill.py` — anchor killed
  under fresh-PTY user shell, daemon survives (the masking case
  that misled the original investigation)
- `e2e/test_sbx_quirk_04_kit_background_true.py` — kit-level
  `background: true` step dies at ~5s regardless of anchor state

### Root cause: anchor death kills startup-launched daemons after 5s

After extensive probing, the precise behavior is:

> **When the `sbx run` anchor process is killed, sbx kills any
> long-running processes started from `commands.startup` exactly
> ~5 seconds later** — unless the user-shell host process happens to
> hold a fresh dedicated PTY on its stdin (in which case sbx spares
> them).

This was isolated with a 4-case probe (pure sbx, devm-rendered kit):

| # | Anchor | User-shell shape | Daemon lifetime |
|---|---|---|---|
| A | alive  | none                       | 31s+ (alive) |
| B | alive  | subprocess.Popen (no PTY)  | 33s+ (alive) |
| C | alive  | pexpect.spawn (fresh PTY)  | 34s+ (alive) |
| D | killed | subprocess.Popen (no PTY)  | **5s (dies)** |
| E | killed | pexpect.spawn (fresh PTY)  | 36s+ (alive — masks the bug) |

Cases A–C prove anchor-alive is sufficient. D and E (E run earlier as
the pure-sbx `pur` probe) prove anchor-kill triggers the 5s kill,
*except* when the user shell holds a fresh PTY.

### Why "shared PTY user shell + anchor death" hits production

In production, `devm shell` runs from the user's real terminal. devm
inherits that terminal's PTY. devm spawns `sbx exec -it bash` via Go
`exec.Cmd` with inherited stdio — so the user shell **shares** the
terminal's PTY with devm rather than getting its own. That puts
production into case D, where the daemon dies at ~5s. The bug isn't
visible in pure-sbx hand-tests with `pexpect.spawn` because pexpect
**allocates a fresh PTY for the child** (case E), masking it.

### Implication: several existing quirks share this root cause

Quirks #2 (ports vanish if published while anchor holds the session)
and #3 (phantom first post-anchor-kill publish) are also
anchor-death-triggered. The handoff dance (spawn user shell → 500ms
settle → kill anchor → post-kill safety check) and the post-handoff
port-reconcile ordering only exist because devm currently kills the
anchor as part of the cold-start choreography.

If devm instead leaves the anchor alive for the lifetime of the
sandbox, all three quirks dissolve simultaneously, and the user
shell can be spawned via plain inherited stdio (no PTY proxy
required, no fresh-PTY workaround).

### Possibly-flaky: cross-session anchor + interactive user shell

Initial test_sbx_anchor_07 run observed daemon death (~5s) when the
anchor was setsid'd into its own session AND an interactive user
shell ran in a different session. On stability re-runs the death did
NOT reproduce — 4/5 attempts the daemon survived 20s+. The behavior
is flaky rather than deterministic, so:

  - same-session anchor (Go `exec.Cmd` default) is the safer choice
    because we have many test runs of it surviving;
  - setsid'd anchor has been seen to break daemons but doesn't
    reliably break them — production isn't blocked from using it.

e2e/test_sbx_quirk_03_setsid_anchor_kills_daemon.py is currently
an observation test (records outcome but doesn't assert death).
If the death pattern is pinned down, it'll become a hard assertion.

### Refinement: anchor must ignore SIGHUP to survive terminal close

If the user closes their terminal window (rather than typing `exit`),
the kernel closes the master PTY. The kernel then sends SIGHUP to
processes whose controlling tty was that PTY. The default SIGHUP
action is terminate — so a default-disposition anchor *can* die,
which stops the sandbox. setpgid alone does not save the anchor (the
SIGHUP is delivered based on controlling-tty membership, not
foreground-PG membership). The reliable fix is to install
`SIG_IGN` for SIGHUP on the anchor before exec — either via Go's
`signal.Ignore(syscall.SIGHUP)` before `cmd.Start()`, or by spawning
as `nohup sbx run ...`.

Whether default-disposition anchors die on terminal-close is flaky
(it sometimes survives in isolation but not under concurrent load).
Don't rely on it. Pinned by
e2e/test_sbx_anchor_10_terminal_close.py — the `must_survive`
shapes (`ignhup_only`, `setpgid_ignhup`) are asserted; the `flaky`
shapes (`default`, `setpgid`) are merely recorded.

### Pgrep-self-match guidance (testing convenience)

When writing tests that check whether a process matching a marker
string is running inside a sandbox, use:

```sh
pgrep -af MARKER 2>/dev/null | grep -v pgrep | grep -q . && echo OK || echo MISS
```

The `grep -v pgrep` filters out the `sh -c "pgrep -af MARKER ..."`
line which always self-matches. Without that filter, every check
returns OK regardless of whether a real process is running — and
several earlier devm tests turned out to have been passing only
because of this false positive.

## 6. devm port-reconcile destabilizes the sandbox endpoint (RESOLVED 2026-06-04)

**Root cause:** Two structural choices in `internal/orchestrator/
shell.go` together destabilized the sandbox endpoint at cold-
start, making the first `sbx ports --publish` phantom in ~80% of
runs:

  1. The `waitForRunning` helper used a `time.NewTicker(poll)` +
     `select { ctx.Done / runDone / ticker.C }` loop, parking the
     goroutine on a blocking multi-channel select between every
     `sbx ls` poll.
  2. The anchor (`nohup sbx run …`) was spawned through the
     `Spawner` / `SpawnedCmd` interface (`ExecSpawner.Start`),
     wrapping `*exec.Cmd` in `&execCmd{c: c}` and escaping it
     through interface return.

Either alone is insufficient — bisection scored strip-with-(1)-
fixed-only at 8/9 publish-OK. The pair together hit 10/10. The
empirical mechanism is unclear (the SpawnedCmd indirection and
the ticker+select loop are both behaviorally equivalent at the
syscall level to the probe's inline raw-`*exec.Cmd` + plain
`for { check(); time.Sleep(250ms) }`), but the bisection is
decisive: bypassing both produces a stable endpoint, anything
less produces the phantom.

**Fix:** In `RunShell` —

  - Spawn the anchor inline as a raw `*exec.Cmd` (no Spawner
    indirection). `ShellDeps.AnchorSpawner` is now nil in
    `DefaultShellDeps`; tests that inject a stub still go
    through the interface path (which doesn't trigger the bug
    because tests don't create real `*exec.Cmd` for the anchor).
  - Replace the `waitForRunning` helper call with an inline
    `for { check(); time.Sleep(d.PollInterval) }` loop. A
    non-blocking `select { case <-runDone: …; case <-ctx.Done():
    …; default: }` preserves anchor-death and ctx-cancellation
    detection without re-introducing the blocking multi-channel
    select. The standalone `waitForRunning` function is deleted
    so future code can't pull it back in.

**Pinned by:**

  - `e2e/test_07_invariant_happy_path.py` — green
  - `e2e/test_sbx_anchor_14_publish_trigger_pin.py` — a probe
    binary (`probe-publish-triggered`) that re-applies both
    trigger pieces around the same sbx flow and FAILS the
    publish/observe assertion with high probability, pinning
    the trigger so a future devm refactor can't quietly
    reintroduce it.

The previous OPEN write-up + bisection log is preserved below
for archaeology.

---

### Original report (OPEN — 2026-06-02)

**Observed:** Under the anchor-alive architecture, `devm shell`
cold-start consistently fails to durably publish canonical
ports. The orchestrator's `publishWithVerify` correctly detects
the phantom (`vanished during hold — loop`), but the first
publish itself appears to tear down the sandbox's port endpoint,
after which all subsequent publishes get:

```
ERROR: publish port: failed to resolve endpoint:
       no container endpoint with IP address found
```

…for the full 30s `publishWithVerify` deadline, then return an
error. devm continues without the port. test_07 fails 5/5.

**Pure-sbx is NOT affected.** A bisection probe (`e2e/probes/
probe-publish/main.go`, exercised by
`e2e/test_sbx_anchor_12_go_probe_publish.py`) does literally
everything devm's `RunShell` does — spawn nohup-wrapped anchor,
wait running, wait exec-ready, list ports, publish, tight-poll
verify, 500ms hold, reverify, snapshot-style `sbx exec`, user-shell
spawn, 10+s observation — and produces a durable mapping on
**every** run, with both `--nohup` and plain anchor. So:

  - The `sbx` CLI behavior is fine (pure-sbx tests + probe agree).
  - Go's `exec.Cmd` is fine (the probe is a Go binary).
  - The kit content is fine (probe uses the same `materialize_kit`
    spec; also verified earlier with the actual devm-rendered kit).
  - The nested-vs-separate kit/workspace structure is fine.
  - The user-shell spawn (`sbx exec -it`) is fine.
  - Tight-poll verify cadence (250ms) is fine.

What's left in devm's flow that the probe DOESN'T have:
file-lock (`flock` on `.devm/lock`), `render.WriteDevmDir` (which
overwrites the kit before anchor spawn), and the `runDone`
goroutine that calls `runCmd.Wait()` on the anchor. None of these
should plausibly affect sbx daemon state — but **something** in
devm's larger orchestrator is the active ingredient.

### Bisection findings (2026-06-03/04)

Worked the disposable `strip-devm-publish` branch. Ruled out as
*the* cause (each tested in isolation, bug still reproduces):

  - `lock.Acquire` / lock release (stripped)
  - `render.WriteDevmDir` (stripped, test pre-renders)
  - `runDone` goroutine + `runCmd.Wait()` (stripped, replaced
    with a never-firing channel)
  - `ReconcilePortsWithRunner` complexity (replaced with single
    direct `exec.Command("sbx", "ports", ... --publish ...)`)
  - `WriteSnapshot` (stripped)
  - `signal.NotifyContext(SIGINT)` in `cmd/devm/shell.go`
    (replaced with bare `context.Background()`)
  - cobra command framework (verified by building a *cobra-
    wrapped probe* — same flow as `e2e/probes/probe-publish`
    but driven via cobra+`signal.NotifyContext`; passes 5/5)
  - Kit content / project ID / agent name / cwd of the anchor
    (verified by building a probe variant that uses devm's
    rendered `.devm/spec.yaml`, devm's `cfg.Project.ID` as
    agent name, and cwd=workspace; passes 10/10)

**Pure-sbx (Python `subprocess.Popen` with inherited PTY for
`sbx exec -it`, *no delay* between `sbx ports --publish` and
`sbx exec -it`) PASSES 5/5** with both the minimal anchortest
kit and the devm-rendered kit. The bug does **not** reproduce in
pure-sbx at the publish-then-exec sequence level. The
regression-guard test hoped for in the original handoff is not
constructible from pure-sbx.

`lsof` on the anchor *did* show devm's anchor holds an extra
established TCP to `*.compute-1.amazonaws.com:https` plus a ctl
socket and unix-domain socket that the probe's anchor doesn't.
**That difference is symptomatic, not causal** — the
probe-with-devm-kit variant lacks those fds and still publishes
reliably; the fds correlate with which sandbox name / kit
combination triggers `sandboxes-auth` to keep a connection warm,
but they don't cause the publish to fail.

**Mixed signal on timing fixes** (each row is 10 runs of
test_07; "pass-publish" = port appeared within
`wait_for_port_published`'s 30s window, regardless of the
unrelated "never reached stopped" anchor-alive test artifact):

| Variant | pass-publish |
|---|---|
| strip baseline (no fix) | 1/10 |
| strip + 200ms sleep *before* `waitForRunning` | 4/5 |
| strip + 1000ms sleep *before* `waitForRunning` | 1/5 |
| strip + 1.5s sleep *after* publish, before user shell | 2/10 |
| strip + inline `waitFor` + bypass `ExecSpawner` for anchor + 500ms post-publish | 10/10 |
| main + single direct publish + 500ms post-publish | 1/5 |
| main + 500ms sleep before user shell only | 3/10 |

The only configuration that hit 10/10 was a *combined* refactor
on strip: inline polling loops + bypassing `ExecSpawner` for the
anchor + 500ms post-publish settle. Individual changes are
within noise of the ~20% baseline. So the bug is **diffuse**:
several layers conspire; no single piece is load-bearing on its
own.

**Still untested as the trigger** (the structural items that
remain in stripped-devm but aren't in the working probe):

  - `sandbox.Sandbox` struct's `IsRunning()`/`Exists()` (parse
    `sbx ls` output) instead of probe's inline check
  - `waitForRunning` helper (uses `time.NewTicker` + `select`
    with the `runDone` channel) instead of probe's inline
    `for { check(); time.Sleep(250ms) }`
  - `waitForExecReady` helper (similar shape)
  - `ExecSpawner.Start` indirection (returns a `SpawnedCmd`
    interface) instead of probe's direct `exec.Cmd.Start()`
  - `userCmd.Wait()` blocking at the end of `RunShell` instead
    of the probe's observation loop

**Status:** RESOLVED 2026-06-04 by the combined refactor
hinted at in the previous paragraph. The 500ms post-publish
settle turned out to be a red herring: inline `waitForRunning`
+ bypass `ExecSpawner` alone scored 10/10 publish-OK on
test_07 with NO sleep, beating the partial fixes by ~10
percentage points and the baseline by 80. See top of section
for the current write-up.

**Pinned by:**

- `e2e/test_sbx_anchor_12_go_probe_publish.py` — Go binary works
  reliably (both nohup and plain) when invoked via pexpect
- `e2e/test_sbx_anchor_13_publish_stability.py` — pure-sbx
  publish stability under multiple patterns; pins that subsequent
  republishes correctly return `already published` (NOT `no
  container endpoint`)
- `e2e/test_07_invariant_happy_path.py` — the end-to-end devm
  failure; this is what flips green when Quirk #6 is resolved

**Workaround in `publishWithVerify`:** retry on the documented
`no container endpoint` transient with 500ms backoff inside the
30s deadline. Pinned by
`TestPublishWithVerifyRetriesOnEndpointNotReady`. **This is
necessary but not sufficient** — the underlying endpoint loss
prevents recovery within the deadline window.
