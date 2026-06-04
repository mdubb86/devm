"""sbx-anchor 14: pin Quirk #6's trigger — a Go probe binary that
re-applies the two structural pieces RunShell used to have around
the anchor spawn (Spawner / SpawnedCmd interface wrapping for the
anchor + ticker+select waitForRunning) FAILS to publish reliably,
even though the otherwise-identical clean probe (test_sbx_anchor_
12_go_probe_publish.py) succeeds 10/10.

The asymmetry between the clean probe and this triggered probe
**positively pins the trigger** for Quirk #6. If a future refactor
of `internal/orchestrator/shell.go` quietly puts either trigger
piece back around the anchor spawn, three things should happen
together:

  1. test_07_invariant_happy_path.py goes red (the real bug).
  2. THIS test stays green (the trigger still reproduces in the
     probe).
  3. test_sbx_anchor_12_go_probe_publish.py stays green (the
     clean probe still passes — pins that sbx itself is fine).

If THIS test goes red (probe stops failing), that's a sign sbx's
behavior has shifted and Quirk #6's resolution should be
re-examined: see docs/sbx-quirks.md section 6 and revisit the
2026-06-04 bisection findings.

The probe binary lives at e2e/probes/probe-publish-triggered/
main.go.

Cadence note: at the bisected baseline (~20% publish-OK on the
strip branch), the probability of all 3 runs accidentally passing
is ~0.8%, so "at least 1 of 3 FAIL" is a high-confidence assertion
(~99.2% reliability) of the trigger being live. If sbx ever speeds
up enough that the trigger's race window closes (a real and
welcome regression of the bug), this test will start flaking — at
which point delete it and Quirk #6 along with it.
"""
from __future__ import annotations
import os
import subprocess
import tempfile

import pexpect
import pytest

from helpers import sbx
from helpers.sbx_kit import materialize_kit


REPO_ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def _build_triggered_probe() -> str:
    binpath = os.path.join(tempfile.gettempdir(), "devm-probe-publish-triggered")
    r = subprocess.run(
        ["go", "build", "-o", binpath, "./e2e/probes/probe-publish-triggered/"],
        cwd=REPO_ROOT, capture_output=True, timeout=60,
    )
    if r.returncode != 0:
        pytest.fail(
            f"go build of probe-publish-triggered failed: "
            f"stdout={r.stdout!r} stderr={r.stderr!r}"
        )
    return binpath


def _run_triggered_once(binpath: str, sandbox_name: str) -> tuple[int, str]:
    """Run the triggered probe once via pexpect; return (exitcode, output)."""
    kit = materialize_kit()
    try:
        args = [binpath, "--nohup", kit.kit_dir, kit.workspace, sandbox_name]
        child = pexpect.spawn(
            args[0], args[1:], encoding="utf-8", timeout=180, dimensions=(40, 200),
        )
        try:
            child.expect(pexpect.EOF, timeout=180)
            out = child.before or ""
            child.close()
            return (child.exitstatus or 0), out
        finally:
            try:
                child.close(force=True)
            except Exception:
                pass
    finally:
        sbx.sandbox_rm(sandbox_name)
        kit.cleanup()


@pytest.mark.timeout(420)
def test_triggered_probe_fails_reliably(sandbox_name):
    """Run the triggered probe 3 times; assert at least 1 fails.

    See module docstring for why "at least 1 of 3" is the right
    assertion: with the bisected ~20% publish-OK baseline,
    P(all 3 pass) ≈ 0.8%, so this is ~99.2% reliable.
    """
    binpath = _build_triggered_probe()
    results = []  # list of (rc, output_tail)
    for i in range(3):
        name = f"{sandbox_name}-tr{i}"
        rc, out = _run_triggered_once(binpath, name)
        tail = "\n".join(out.splitlines()[-6:])
        results.append((rc, tail))

    print("\n=== probe-publish-triggered 3-run results ===")
    for i, (rc, tail) in enumerate(results):
        status = "PASS" if rc == 0 else f"FAIL (rc={rc})"
        print(f"  run {i}: {status}")
        print(f"    tail:\n      " + tail.replace("\n", "\n      "))
    print("=== END ===\n", flush=True)

    failures = [(i, rc) for i, (rc, _) in enumerate(results) if rc != 0]
    assert failures, (
        "probe-publish-triggered passed 3/3 — Quirk #6's trigger no "
        "longer reproduces. Either sbx's behavior has shifted (good "
        "news; re-examine docs/sbx-quirks.md section 6 and possibly "
        "delete this test) or the trigger pieces in the probe aren't "
        "actually triggering anymore (see e2e/probes/probe-publish-"
        "triggered/main.go for the two pieces — Spawner interface "
        "wrapping and ticker+select waitForRunning)."
    )
