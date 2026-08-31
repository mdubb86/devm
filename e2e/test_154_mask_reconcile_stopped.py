"""154: mask added while VM stopped is picked up on next devm shell.

Cold-start without a mask, stop, add masks: [scratch] to devm.yaml,
run devm shell. The cold-start path picks up the top-level Masks
declaration and renders the mount script; the mask must be active
on the new session without any manual reconcile step.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_mask_added_while_stopped_picked_up_on_next_shell(devm, workspace, sandbox_name):
    workspace.write_devmyaml(install=["true"])
    try:
        # Cold-start without mask.
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0

        # Stop, preserving disk.
        subprocess.run(
            [devm.path, "stop", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )

        # Add the mask while stopped.
        devm.unlock()
        workspace.patch_devmyaml(install=["true"], masks=["scratch"])

        # Cold-start again — the mask should be mounted.
        r = subprocess.run(
            [devm.path, "shell", "--", "sh", "-c",
             f"mountpoint -q {workspace.path}/scratch && echo mounted || echo NOT_MOUNTED"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"post-restart shell failed:\n{r.stderr.decode()}"
        assert "mounted" in r.stdout.decode(), (
            f"mask was not mounted after re-shell:\n{r.stdout.decode()!r}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
