# Handoff: async-runtime-death during devm cold-start

**Status (2026-06-05):** suite green, immediate cold-start regression
reverted, dogfood **still broken** for projects with non-trivial
install: steps (everstone hits it). Underlying race needs a real fix.

## The bug in one paragraph

Under devm's `RunShell` cold-start flow, when `install:` is non-trivial
(takes more than ~1s), the sbx runtime dies asynchronously between
"sandbox briefly reaches running" and the moment devm tries to do
something with it. Surface error varies by timing:
`port reconcile failed: ... runtime "<name>" not found`,
`write snapshot: exit status 1: ERROR: no sandbox named '<name>'`,
or user-shell-spawn failure. Anchor output (now surfaced via ring
buffer + `formatAnchorOutput` decoration in the cleanup defer) always
shows:

```
ERROR: failed to create sandbox: ... start container: started hook:
container exec create: Error response from daemon: container ... is not running
... (repeats)
create directory /<parent-of-workspace>:
write file /<workspace>/../CLAUDE.md:
```

That last line is sbx's `aiFilename` machinery trying to write CLAUDE.md
at `<workspace>/../` — possibly an sbx-side bug, but it's fallout from
the container already being dead, not the cause.

## What's been ruled out (don't waste time here)

Each ruled out by a pure-sbx test or hypothesis edit + e2e re-run:

| Suspect | Verdict | Pin |
|---|---|---|
| `nohup` wrapper | INNOCENT | `test_sbx_05::test_nohup_wrapped_brings_up` |
| `Stdin = nil` (DEVNULL) | INNOCENT | manual: `sbx run … < /dev/null` worked |
| Ring buffer for stdout/stderr | INNOCENT | edit `Stdout = nil` in shell.go → same failure |
| `apt-get update` command itself | INNOCENT | `test_sbx_04` runs it under direct sbx; passes |
| Multiple install commands | INNOCENT | `test_sbx_05::test_nohup_wrapped_brings_up` (apt + touch) |
| devm-shape startup steps (`init-volumes.sh`, `install-templates.sh`) | INNOCENT | `test_sbx_05::test_nohup_plus_devm_shape_startup` |
| `$WORKSPACE_DIR` not expanded at install time | INNOCENT | `test_sbx_03` proves WORKSPACE_DIR is set + workspace mount visible |
| allowed_domains blocking apt | INNOCENT | `test_sbx_04` — sbx allows apt regardless |
| Kit-inside-workspace (`--kit <workspace>/.devm`) | INNOCENT | `test_sbx_05::test_nohup_with_kit_inside_workspace` |
| Snapshot-style sbx exec post-bringup | INNOCENT | `test_sbx_05::test_nohup_plus_snapshot_style_exec_during_install` |
| YAML quoting style (bare vs single-quoted) | INNOCENT | tested both in `test_sbx_05` |

**The critical fact**: the ENTIRE shape of devm's spec.yaml + spawn +
post-spawn ops works in pure-sbx tests. The bug only manifests under
`devm shell`'s full RunShell flow.

## The smoking gun

`DEVM_DEBUG=shell ~/go/bin/devm shell` against a devm.yaml with
`install: [apt-get update, touch /home/agent/marker-a]` and NO services
showed:

```
[devm-shell ...] cold-start: sandbox status=running    +4.0s
[devm-shell ...] cold-start: exec-ready                +1.8s
[devm-shell ...] port-reconcile: starting              +0.0s
[devm-shell ...] port-reconcile: done                  +1.1s
[devm-shell ...] snapshot: writing                     +0.0s
[devm-shell ...] snapshot: done                        +7.8s    ← suspicious
[devm-shell ...] spawning user shell: sbx [exec -it …]
[devm-shell ...] user shell spawned pid=…              +0.002s
ERROR: failed to start sandbox: sandboxd error: status 404: runtime "…" not found
user shell exited rc=1
```

`snapshot: done` taking 7.8s instead of typical ~50ms strongly implies
`sbx exec NAME sh -c "…"` (WriteSnapshot's call) was queued behind
something — probably sbx daemon serializing exec calls until install
finishes. **Runtime survives all of that, then dies in <2ms before the
user-shell `sbx exec -it`.**

## Reproducer

```bash
# minimal: ~13s to fail
cd $(mktemp -d)
cat > devm.yaml <<'EOF'
project:
  id: repro
  sandbox_name: repro
  hostname_apex: repro.local
  port_offset: 60000
base_image:
  docker: false
install:
  - apt-get update
EOF
sbx rm -f repro 2>/dev/null
DEVM_DEBUG=shell ~/go/bin/devm shell < /dev/null
```

Or any everstone-shaped devm.yaml with a slow install step.

## Recent commits touching the area

```
8e950b3 fix(shell): every cold-start failure surfaces anchor output
6e6ef42 refactor(shell): single defer for cold-start cleanup
df4083d fix(shell): all cold-start / attach failures are now fatal
ab24f59 fix(shell): port reconcile failure during cold start is fatal
e0a8ed1 fix(sandbox): Run/RunStdin fold stderr into error
a7ad5a3 feat(scripts): bootstrap.sh embedded install (REVERTED via …)
…and the revert: HEAD~1 drop bootstrap.sh prepend
```

The fatal-failure changes (df4083d, ab24f59) DID NOT introduce the
race — they just made it surface as a hard error instead of a silent
continue. The underlying async death has been there since the
anchor-alive cold-start architecture was built.

## Files to know

| File | Why |
|---|---|
| `internal/orchestrator/shell.go` | `RunShell` — the broken flow |
| `internal/orchestrator/snapshot.go` | `WriteSnapshot` — currently does `sbx exec NAME sh -c "mkdir + base64 + mv"`. Candidate for host-side rewrite. |
| `internal/orchestrator/ports.go` | `waitForExecReady` (the readiness gate that passes too early) and port reconcile |
| `internal/render/spec.go` | spec.yaml renderer. Could append an install-completion marker. |
| `internal/scripts/bootstrap.sh` | embedded but currently NOT prepended in install: (reverted). Keep — re-enable once race is fixed. |
| `e2e/test_sbx_05_nohup_install_interaction.py` | 5-test fleet that pins what's NOT the bug |
| `e2e/test_24_cold_start_docker_base.py` | DinD base cold-start (currently passes — was broken by bootstrap prepend) |
| `e2e/test_25_cold_start_curl_install.py` | curl|bash install cold-start (currently passes) |
| `docs/sbx-quirks.md` | known sbx quirks. Add this race once understood. |

## Two candidate fixes (preferred order)

### A) Move snapshot to host-side (RECOMMENDED first attempt)

Snapshot's job: record "last applied" cfg state for next reconcile's
diff. The cfg is already on the host. Writing to
`<repoRoot>/.devm/applied.yaml` instead of
`/home/agent/.devm/applied.yaml` via `sbx exec` removes ONE `sbx exec`
call from the cold-start window AND moves the persistent state to
host-side (where it can be gitignored, survives sandbox rm, etc).

Changes:
- `internal/orchestrator/snapshot.go`: `WriteSnapshot` writes to a host
  path. `ReadSnapshot` reads from a host path. No sbx exec.
- `internal/orchestrator/shell.go`: snapshot step becomes a fast local
  file write. The race window shrinks; user shell spawn happens closer
  to exec-ready.
- This MAY be enough on its own. If runtime still dies, see (B).

### B) Install-completion marker readiness gate

Replace `waitForExecReady`'s `sbx exec NAME true` probe with a probe
for a file the LAST install step touches. Append at render time:

```yaml
commands:
  install:
    - command: <user step 1>
    - <user step N>
    - command: touch /home/agent/.devm-install-complete   # appended by devm
```

Then `waitForExecReady` polls `sbx exec NAME test -f /home/agent/.devm-install-complete`. Install completes → marker exists → ready.

For warm path (sandbox already running, install ran at create), the
marker exists from create time, so the check succeeds immediately.

## What we don't know

- **Why exactly the runtime dies async.** Best guess: sbx daemon has
  some lifecycle transition between install-complete and
  entrypoint-ready that briefly invalidates the runtime. Pure-sbx tests
  don't catch it because they aren't trying to use the runtime in that
  exact instant.
- **Whether (A) alone is sufficient.** Worth trying first because it's
  the cheaper change AND a worthwhile design improvement on its own.
- **Why sbx writes CLAUDE.md at `<workspace>/../CLAUDE.md`.** Possibly
  an sbx bug. Visible in every anchor-output dump. Filing upstream
  worth considering, but it's a SYMPTOM not the cause.

## Test to write that pins the fix

A new e2e test that catches this regression deterministically:

```python
# e2e/test_26_cold_start_slow_install.py
def test_cold_start_with_slow_install_no_async_death(workspace, devm, sandbox_name):
    """Pins the 2026-06-05 async-runtime-death race. Install: with
    apt-get update should not break cold start."""
    workspace.write_devmyaml(install=["apt-get update"])
    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=120)
        sh.exit(timeout=30)
    devm.stop(yes=True)
```

This test will FAIL today against HEAD. Mark it `@pytest.mark.xfail(strict=True, reason="...")`
until the fix lands. When the fix works, remove the xfail and the test
PASSES — guarding the regression forever.

## Immediate user workaround (already shared)

User can manually start the sandbox via `sbx run`, then `devm shell` in
another terminal takes the WARM path (sandbox already running →
RunShell skips cold-start orchestration entirely → attaches cleanly).
Not a fix, just an unblock.

## How to start

1. Read this file.
2. Confirm reproducer fails on HEAD (commit at top of `git log`).
3. Try fix A (host-side snapshot). Run `e2e/test_26_cold_start_slow_install.py`. If it passes, ship.
4. If it doesn't pass, also do fix B (install-completion marker). That should be sufficient.
5. Once green, re-enable bootstrap.sh prepend in `internal/render/spec.go` (it's the same `spec.Commands.Install = append(…)` line, currently commented with the revert reason).
6. Run full e2e (`just e2e`, ~30 min). Update `docs/sbx-quirks.md`.
