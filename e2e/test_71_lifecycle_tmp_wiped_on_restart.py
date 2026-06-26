"""71: /tmp is WIPED on VM stop + restart (inverts the sbx-era pin).

This inverts the sbx-era pin (test_sbx_contract_26_lifecycle_tmp_survives_sbx_stop_restart),
where /tmp survived `sbx stop` + `sbx run` because sbx used container-pause semantics.

With Tart, `tart stop` + `tart run` is a real VM shutdown/boot cycle. /tmp is on
tmpfs and is wiped on boot. Consumers who relied on /tmp persistence across stop+start
should use $WORKSPACE_DIR or a declared mounts: entry instead.

A secondary consequence: the sbx-era startup-supervision hack that explicitly ran
`rm -rf /tmp/.devm` at the head of each start is no longer needed. Tart's boot cycle
gives us a clean /tmp for free.

What this pins:
  - A file written to /tmp before `devm stop --yes` is ABSENT after `devm shell`
    brings the VM back to running (i.e., stop+start is a real boot, not a resume).

What it doesn't cover (tested elsewhere):
  - Disk state (non-/tmp) persisting across stop/restart -> test_52.
  - Volume/mount data persisting across stop/restart -> test_70.
"""
from __future__ import annotations

import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_tmp_wiped_on_stop_restart(workspace, devm, tart_sandbox):
    # tart_sandbox fixture already cold-started the VM.
    assert tart_sandbox.state() == "running", (
        f"expected VM running after cold-start; got {tart_sandbox.state()!r}"
    )

    # Write a probe file to /tmp.
    r = tart_sandbox.exec_shell("echo probe > /tmp/probe-tmp")
    assert r.exit_code == 0, f"failed to write probe file: {r.stderr!r}"

    # Sanity: present immediately.
    r = tart_sandbox.exec("test", "-f", "/tmp/probe-tmp")
    assert r.exit_code == 0, "/tmp/probe-tmp should exist immediately after write"

    # Stop the VM.
    devm.stop(yes=True, timeout=30)

    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if tart_sandbox.state() == "stopped":
            break
        time.sleep(0.5)
    assert tart_sandbox.state() == "stopped", (
        f"VM should be stopped after devm stop; got {tart_sandbox.state()!r}"
    )

    # Restart via devm shell -- true (cold-restart / real boot cycle).
    subprocess.run(
        [devm.path, "shell", "--", "true"],
        capture_output=True, cwd=str(workspace.path), timeout=180,
    )

    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        if tart_sandbox.state() == "running":
            break
        time.sleep(0.5)
    assert tart_sandbox.state() == "running", (
        f"VM should be running after restart; got {tart_sandbox.state()!r}"
    )

    # Probe file must be ABSENT — Tart's boot cycle wipes /tmp.
    r = tart_sandbox.exec("test", "-f", "/tmp/probe-tmp")
    assert r.exit_code != 0, (
        "/tmp/probe-tmp is still present after stop+restart — "
        "Tart is no longer providing a clean /tmp on boot. "
        "If this is expected, update the supervision design to re-add "
        "the explicit `rm -rf /tmp/.devm` cleanup step at startup."
    )
