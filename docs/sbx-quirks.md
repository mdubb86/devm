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

## 2. Ports published while the `sbx run` anchor holds the session get torn down

**Observed:** During cold start, devm published ports (`sbx ports
--publish`) *before* killing the `sbx run` anchor. The publish returned
success ("Published 127.0.0.1:X -> Y/tcp") and was briefly visible, but
once the anchor was killed during handoff the mapping vanished from
`sbx ports --json`. Sandboxes with a background startup daemon happened
to mask this; minimal single-port sandboxes exposed it (the port never
appeared).

**Workaround:** `internal/orchestrator/shell.go` — port reconcile is
deferred until *after* the anchor is killed and the safety check
confirms the sandbox is still up (steady state, only the user session
attached). Ports published in steady state are not tied to the anchor
session and survive.

## 3. `sbx ports --publish` success can precede listing visibility

**Observed:** Right after a sandbox reaches `status: running`,
`sbx ports --publish` may return success while the mapping doesn't yet
appear in `sbx ports --json` — a brief readiness window.

**Workarounds (layered):**
- `internal/orchestrator/shell.go` `waitForExecReady` — after sbx
  reports `running`, poll `sbx exec <name> true` until it succeeds
  before proceeding. sbx's running status precedes full readiness.
- `internal/orchestrator/ports.go` `publishWithVerify` — after
  publishing, poll `sbx ports --json` to confirm the mapping is live;
  re-issue the publish if not, tolerating ONLY the specific
  "already published" error.

The "already published" error text (for matching) is:
`publish port: port 127.0.0.1:<host>/tcp is already published`.

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
- The e2e suite (`make e2e`) exercises the workarounds end-to-end;
  `test/e2e/08_reconcile_live_port.exp` is the regression guard for
  the port behaviors.
