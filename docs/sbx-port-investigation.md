# sbx Port-Publish Behavior — Investigation Notes

Living document tracking what we have **empirically confirmed** about how
sbx handles port publishing across session transitions, and how that
intersects with devm's cold-start orchestration. Each claim here is
backed by a runnable test under `e2e/test_sbx_*.py` (sbx-only) or a
reproducible devm probe.

**Status:** root cause identified, fix landed. Last update: 2026-06-01.

## TL;DR

- sbx itself is **not** session-scoped for port mappings. Ports
  published on a sandbox with ≥1 session survive when one session
  ends, as long as the sandbox stays alive on another session.
- sbx **does** have a phantom-publish race on the first
  `ports --publish` call right after a session disconnect: the call
  returns "Published ..." and the mapping briefly appears in
  `--json`, but it never durably applies. The **second** publish is
  the one that actually sticks.
- devm's `publishWithVerify` trusted the first verify-true and stopped
  retrying, hitting the phantom in ~80% of cold starts. A "hold and
  re-verify" defense in `publishWithVerify` makes the second publish
  always fire when the first was a phantom — 10/10 OK with the fix.

## Test inventory

| Test | What it does | Result |
|---|---|---|
| `test_sbx_01_port_survives_session_swap.py` | Built-in `shell` agent. Anchor (sbx run) via pexpect → publish → spawn `sbx exec -it bash` → kill anchor → check port at +3s AND +33s, end-to-end TCP each time. | **5/5 PASS** |
| `test_sbx_02_custom_kit_port_survives.py` | Same as sbx-01 but with a hardcoded minimal custom kit (sleep-infinity entrypoint, derived from `internal/render/spec.go` shape). | **5/5 PASS** |
| ~~`test_sbx_03_devm_sequence_replay.py`~~ | *(Deleted 2026-06-02. Replayed the OLD cold-start sequence; superseded by the anchor-alive design — see `e2e/test_sbx_anchor_*.py`.)* | — |

| Devm probe | Configuration | Result |
|---|---|---|
| devm cold-start, no extra wait | `DEVM_PROBE_POST_KILL_SLEEP_MS=0` (default; publish happens immediately after `killRun`) | 3/5 OK |
| devm cold-start, 1000 ms wait | `DEVM_PROBE_POST_KILL_SLEEP_MS=1000` | 5/5 OK |
| devm cold-start, 3000 ms wait | `DEVM_PROBE_POST_KILL_SLEEP_MS=3000` | 5/5 OK |
| devm cold-start, 5000 ms wait | `DEVM_PROBE_POST_KILL_SLEEP_MS=5000` | 5/5 OK |
| devm cold-start, publish BEFORE handoff | `DEVM_PROBE_PUBLISH_PRE_HANDOFF=1` (publish runs while anchor still alive, before user shell spawn) | 0/8 OK — port visible immediately after `killRun` but evaporates within ~35s |

## Established facts

### 1. sbx does NOT tie port lifetime to a specific session

Test: sbx-01. With session A holding the sandbox and a port published,
attaching session B and then closing A leaves the port intact. End-to-end
TCP traffic continues to flow through the port via session B for at
least 33 seconds after A closes.

This **falsifies** the comment at `internal/orchestrator/shell.go:187-190`:

> "Ports published while the sbx run anchor holds the session get torn
> down when that session ends; publishing in the steady state (user
> session only) makes them stick."

The observation that prompted this code may have been a misattribution
of a different race.

### 2. Custom kit vs built-in agent: identical port behavior

Test: sbx-02. A minimal hardcoded kit with `agent.image:
docker/sandbox-templates:shell` and a sleep-infinity entrypoint behaves
identically to the built-in `shell` agent for port-publish lifecycle.
Whatever is biting devm is not "sbx treats kits specially."

### 3. CLI replay of devm's sequence works

Test: sbx-03. Running devm's exact post-cold-start sequence via Python
subprocess (no Go involved) — including pipes-not-pty on the anchor,
500ms settle, SIGKILL, publish-with-verify, WriteSnapshot — produces no
port loss across multiple runs. Port persists +33s, end-to-end TCP works.

This is the **surprising** result: the bug does NOT reduce to "devm
makes the wrong sbx calls in the wrong order." Something else specific
to devm's Go execution matters.

### 4. Devm fails consistently without a post-kill wait

Devm baseline (no `DEVM_PROBE_POST_KILL_SLEEP_MS`): roughly 60% failure
rate. Symptom is `verifyMappingVisible` returning `true` (port visible
during the 3s verify poll), but `current` returning `[]` on the very
next `sbx ports --json` call — flicker-then-evict.

### 5. A 1-second post-`killRun` sleep eliminates the failure

Confirmed in sweep: 0ms → 3/5 OK, 1000ms+ → 5/5 OK. 3000ms and 5000ms
also 5/5; 5000ms is no better than 1000ms — once the window passes,
it's passed.

## Confirmed root cause (2026-05-31)

**The first post-`killRun` publish is a phantom.** sbx returns `Published
127.0.0.1:58880 -> 8080/tcp` with rc=0, the mapping is briefly visible
in `sbx ports --json` for a few hundred ms, and then it evaporates.
The mapping never actually takes effect; nothing is listening on the
host port.

Evidence (6 sequential devm baseline runs, `DEVM_PROBE_POST_KILL_SLEEP_MS=0`):

| Run | iter=1 verify | iter=2 verify | Final port | Outcome |
|---|---|---|---|---|
| 1 | false | true | persistent | PASS |
| 2-6 | true | (skipped) | never visible after | FAIL |

The 5/6 failures all happen because devm's `publishWithVerify` saw the
phantom visibility during the 3s `verifyMappingVisible` poll, returned
success, and stopped retrying. The 1/6 success happened because the
phantom poll missed the visibility window — devm retried — the second
publish actually stuck.

So the bug is: **devm trusts the first verify-true**, but post-`killRun`
the first verify-true is unreliable. **The second publish is the one
that actually applies a durable mapping.**

This matches the 1-second `DEVM_PROBE_POST_KILL_SLEEP_MS` workaround:
sleeping past sbx's settle window means the first publish behaves
durably (no phantom), so `iter=1 verify=true` is trustworthy.

## Open questions

### Q1: Why is the first post-kill publish phantom?

This is upstream sbx behavior we can confirm but not yet explain.
Hypotheses (not tested):

- sbx's session-list reconciliation runs on a tick. The post-`killRun`
  state still has the anchor briefly listed; sbx attributes the publish
  to the dying anchor, then reconciles and evicts.
- sbx's port mapping is two-staged: in-memory acceptance (visible in
  `--json`) followed by an iptables/firewall apply. If the apply
  happens during/after a session change, it reconciles to "no
  attributable session" and drops the mapping.

The empirical fact alone is enough to fix devm; the upstream story is
worth filing with the sbx team but not blocking.

### Q2: Why does CLI replay (sbx-03/04) not reproduce the bug?

sbx-03 and sbx-04 share devm's call sequence but pass reliably. The
likely answer is timing: the natural overhead of Python subprocess
spawns + `anchor.wait()` + `sbx ls` between SIGKILL and the first
publish gives sbx enough wall-clock to leave the phantom window.
devm's Go code runs the same sequence faster, landing the first publish
inside the phantom window ~80% of the time.

The phantom-defense fix makes devm's first-publish timing irrelevant:
even if the first publish flickers and evicts, the hold-and-recheck
detects it and retries. So we no longer need to reproduce the timing
exactly to confirm the fix.

### Q2: Is the 1-second sleep a fundamental fix or just covering up the bug?

The sweep shows 1s is monotonically sufficient on this machine. Whether
1s is enough on a slower machine, under load, or on different sbx
versions is unknown.

## Reproduction recipes

### Run sbx-01..03

```sh
e2e/scripts/run.sh -k test_sbx -n 0 -v
```

### Devm baseline (flaky)

```sh
cd e2e && for i in 1 2 3 4 5; do
  DEVM_PROBE_POST_KILL_SLEEP_MS=0 uv run python -c "
import sys, os, tempfile, shutil, subprocess, time
sys.path.insert(0, '.')
from helpers import Devm, Workspace, Shell, sbx
os.environ.setdefault('DEVM_BIN', '/tmp/devm-debug')
os.environ.setdefault('E2E_REGISTRY', tempfile.mktemp(prefix='reg-'))
name = f'e2e-baseline-r${i}-{int(time.time())%10000:04x}'
path = tempfile.mkdtemp(prefix='dbg-')
ws = Workspace(path, slug='base', sandbox_name=name, port_offset=50800)
ws.write_devmyaml(services={'api': {'canonical': 8080}})
devm = Devm('/tmp/devm-debug', cwd=str(ws.path))
try:
    with Shell(devm, cwd=str(ws.path)) as sh:
        sh.expect_prompt(timeout=60)
        time.sleep(35)
        j = subprocess.run(['sbx','ports',name,'--json'],capture_output=True,timeout=5).stdout.decode().strip()
        print('OK' if '58880' in j else 'FAIL')
        sh.exit(timeout=30)
finally:
    sbx.sandbox_rm(name); shutil.rmtree(path, ignore_errors=True)
    try: os.remove(os.environ['E2E_REGISTRY'])
    except FileNotFoundError: pass
" 2>&1 | tail -1
done
```

### Devm with post-kill sleep workaround

Set `DEVM_PROBE_POST_KILL_SLEEP_MS=1000` (or higher) in the
environment. Probe path in `internal/orchestrator/shell.go` between
`killRun()` and `ReconcilePortsWithRunner`.

## Fix and validation

Implemented in `internal/orchestrator/ports.go: publishWithVerify`.
After `verifyMappingVisible` returns true, the code now holds 500ms
and re-checks. If the mapping is still there, real success; if gone,
loop and re-publish (the second publish is durable).

Validated: 10/10 devm baseline cold-starts OK with the fix (vs 1/6
before). Full parallel e2e suite — including the four `test_sbx_*`
sbx-only tests — runs 29/29.

The investigation-only probes (`DEVM_PROBE_POST_KILL_SLEEP_MS`,
`DEVM_PROBE_PUBLISH_PRE_HANDOFF`) have been removed. `DEVM_DEBUG_PORTS`
tracing is retained: cheap (env-gated) and useful for future debugging.
