"""interop: Spawner-interface + ticker+select Go shapes work against sbx.

Go probe that re-applies two structural pieces (Spawner / SpawnedCmd
interface wrapping for the anchor + time.NewTicker + select for
waitForRunning) which historically triggered Quirk #6's publish
phantom on the pre-0.31 sbx daemon.

Under sbx 0.31+ (where Quirk #6 is fixed), this probe runs 3/3
stably — the trigger pieces no longer destabilize publishes. The
test asserts 3/3 PASS. This is the sentinel: if sbx ever regresses
such that those Go primitives re-introduce the publish phantom,
this test fires and we know to:

  1. Re-read docs/sbx-quirks.md section 6 and the 2026-06-04
     bisection findings.
  2. Avoid the Spawner-interface-wrapping + ticker+select
     waitForRunning shape in internal/orchestrator/shell.go until
     sbx is fixed again.

Counterpart: test_sbx_interop_01_publish_baseline.py — same probe
shape without the trigger pieces. If only baseline stays green and
triggered goes red, that's the upstream regression signal.

The probe binary lives at e2e/probes/probe-publish-triggered/main.go.
"""
from __future__ import annotations
import os
import subprocess
import tempfile

import pexpect
import pytest

from helpers import sbx
from helpers.sbx_kit import materialize_kit

pytestmark = pytest.mark.sbx_interop


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
    """Run the triggered probe 3 times; assert ALL 3 PASS.

    Locked behavior (sbx 0.31+): Quirk #6 (publish phantom) is FIXED.
    The trigger pieces still exist in the probe (Spawner interface
    wrapping + ticker+select waitForRunning), but they no longer
    destabilize publishes — 3/3 stable across runs. Before 0.31 the
    assertion was `assert failures` (~99.2% chance ≥1 of 3 failed).
    If sbx regresses, this test fails loud and we know to bring back
    the inline-poll-no-ticker workaround documented in
    docs/sbx-quirks.md section 6.
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
    assert not failures, (
        f"probe-publish-triggered now sees publish phantoms again "
        f"({len(failures)}/3 runs failed) — Quirk #6 may have "
        f"regressed in sbx. Re-evaluate the inline-poll workaround in "
        f"internal/orchestrator/shell.go's cold-start path against "
        f"docs/sbx-quirks.md section 6."
    )
