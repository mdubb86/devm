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

## 2. Port reconcile must happen AFTER the anchor handoff

**Observed:** During cold start, devm needs the `sbx run` anchor
session alive long enough to bring the sandbox up, then hands off to
the user's `sbx exec -it` session before killing the anchor. Empirically,
running `sbx ports --publish` *while the anchor still holds the session*
results in mappings that vanish when the anchor is killed.

**Workaround:** `internal/orchestrator/shell.go` defers port reconcile
until after the anchor is killed and the safety check confirms the
sandbox is still up on the user session.

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

The full investigation log (including the four `e2e/test_sbx_*.py`
tests that pin down sbx's actual behavior) is in
[`docs/sbx-port-investigation.md`](sbx-port-investigation.md).

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
