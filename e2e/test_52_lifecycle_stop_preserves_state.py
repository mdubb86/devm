"""52: devm stop transitions to stopped and preserves disk state.

After cold-start, writing a file inside the VM, then `devm stop --yes`
must:
  1. Transition the VM to 'stopped' state.
  2. Preserve the marker file (disk state survives the stop).

A second `devm shell -- true` brings the VM back to running, and the
marker file must still be present — proving it was a restart, not a
recreate.

What this pins:
  - devm stop --yes transitions running -> stopped.
  - Disk state (marker file) survives the stop.
  - Subsequent devm shell restarts the existing VM (not recreate).

What it doesn't cover (tested elsewhere):
  - Teardown (removal) -> test_53.
  - Interactive stop prompt -> test_03.
"""
from __future__ import annotations

import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.xfail(
    strict=False,
    reason=(
        "devm bug J: devm stop signals SIGTERM to the tart run process rather than "
        "calling tart stop <name> first, so the guest OS does not complete a clean "
        "shutdown and in-flight disk writes are not committed to the image. "
        "Remove xfail when bug J lands."
    ),
)
@pytest.mark.timeout(240)
def test_stop_preserves_filesystem_state(devm, workspace, tart_sandbox):
    # tart_sandbox fixture already cold-started the VM.
    assert tart_sandbox.state() == "running"

    # Write a marker file inside the VM.
    r = tart_sandbox.exec_shell("echo hello > /tmp/marker.txt")
    assert r.exit_code == 0, f"failed to write marker: {r.stderr}"

    # Stop the VM.
    devm.stop(yes=True, timeout=30)

    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if tart_sandbox.state() == "stopped":
            break
        time.sleep(0.5)
    assert tart_sandbox.state() == "stopped", (
        f"VM should be 'stopped' after devm stop; got {tart_sandbox.state()!r}"
    )

    # Restart via devm shell -- true.
    subprocess.run(
        [devm.path, "shell", "--", "true"],
        capture_output=True, cwd=str(workspace.path), timeout=120,
    )

    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        if tart_sandbox.state() == "running":
            break
        time.sleep(0.5)
    assert tart_sandbox.state() == "running", (
        f"VM should be 'running' after restart; got {tart_sandbox.state()!r}"
    )

    # Marker file must have survived the stop/restart cycle.
    check = tart_sandbox.exec_shell("cat /tmp/marker.txt")
    assert check.exit_code == 0, f"marker missing after restart: {check.stderr}"
    assert check.stdout.strip() == "hello", (
        f"marker corrupted after restart: {check.stdout!r}"
    )
