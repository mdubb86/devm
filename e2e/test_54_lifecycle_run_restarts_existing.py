"""54: devm shell reuses disk when restarting a stopped VM (not recreate).

After cold-start + stop, a second `devm shell -- true` must restart the
existing stopped VM — not recreate it from scratch. A marker file
planted before the stop must survive into the restarted VM.

What this pins:
  - devm shell on a stopped VM restarts it (reuses disk, not recreate).
  - Marker file planted before stop survives the stop/restart cycle.

What it doesn't cover (tested elsewhere):
  - Stop transitions -> test_52.
  - Teardown -> test_53.
  - Disk state general contract (stop preserves) -> test_52.
"""
from __future__ import annotations

import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(240)
def test_shell_restarts_existing_stopped_vm(devm, workspace, tart_sandbox):
    # tart_sandbox fixture already cold-started the VM.
    assert tart_sandbox.state() == "running"

    # Plant a marker to verify restart != recreate. sync() forces the
    # write to disk so page-cache races don't mask the restart-vs-recreate
    # signal we're actually pinning.
    r = tart_sandbox.exec_shell("touch /home/devm/restart-marker && sync")
    assert r.exit_code == 0, f"failed to plant marker: {r.stderr}"

    # Stop the VM.
    devm.stop(yes=True, timeout=30)

    stopped_state = tart_sandbox.wait_state("stopped", timeout=15)
    assert stopped_state == "stopped", (
        f"VM should be 'stopped' after devm stop; got {stopped_state!r}"
    )

    # Restart via devm shell -- true.
    subprocess.run(
        [devm.path, "shell", "--", "true"],
        capture_output=True, cwd=str(workspace.path), timeout=120,
    )

    running_state = tart_sandbox.wait_state("running", timeout=30)
    assert running_state == "running", (
        f"VM should be 'running' after restart; got {running_state!r}"
    )

    # Marker survived → it was a restart, not a recreate.
    check = tart_sandbox.exec_shell("test -f /home/devm/restart-marker && echo present")
    assert check.exit_code == 0, (
        "restart-marker missing after stop/restart — devm may have recreated "
        "the VM from scratch instead of restarting it"
    )
    assert check.stdout.strip() == "present", (
        f"unexpected output from marker check: {check.stdout!r}"
    )
