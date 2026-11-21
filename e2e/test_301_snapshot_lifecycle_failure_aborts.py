"""301: a failed on_stop snapshot aborts `devm stop`; --skip-snapshot
bypasses the gate so the operator can get out.

Flow:
  1. Declare one snapshot entry `broken` with `on_stop: true`. The
     backup hook exits 2 after printing a diagnostic.
  2. Cold-start — VM running.
  3. `devm stop` — must exit non-zero, the diagnostic ("broken",
     "exit 2") must surface in output, and the VM must still be
     running (no premature shutdown on failure).
  4. `devm stop --skip-snapshot` — exits 0; VM stops.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


_DEVM_SH = """#!/usr/bin/env bash
set -eo pipefail
backup_broken()  { echo "intentional failure"; exit 2; }
restore_broken() { cat > /dev/null; }
"""


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_lifecycle_failure_aborts_stop(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        no_repo=True,
        snapshots={
            "broken": {"on_stop": True},
        },
    )
    workspace.write_devm_sh(_DEVM_SH)

    sandbox = TartSandbox(name=sandbox_name)

    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=600,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"
    assert sandbox.state() == "running", "VM should be running after cold-start"

    # Failing on_stop snapshot aborts stop.
    result = devm.stop(timeout=60, check=False)
    assert result.returncode != 0, (
        "devm stop should fail when an on_stop snapshot errors; got rc=0"
    )
    combined = result.stdout.decode() + result.stderr.decode()
    assert "broken" in combined, (
        f"stop output should name the failing entry 'broken'; got:\n{combined}"
    )
    assert "exit 2" in combined, (
        f"stop output should surface the hook's exit code; got:\n{combined}"
    )

    # VM must still be running — the lifecycle gate fired before stop.
    assert sandbox.state() == "running", (
        f"VM should still be running after a failed on_stop snapshot; "
        f"state={sandbox.state()!r}"
    )

    # --skip-snapshot bypasses the gate and completes the stop.
    devm.stop(skip_snapshot=True, timeout=60)
    final = sandbox.wait_state("stopped", timeout=30)
    assert final == "stopped", (
        f"VM should be stopped after `devm stop --skip-snapshot`; got {final!r}"
    )
