"""146: adding a volume while the VM is stopped is picked up on
next `devm shell` without a manual reconcile step.

Cold-start, stop, add a volume to devm.yaml, then run `devm shell`.
The next cold-start must include the new volume in its tart --dir
args and bind it at the declared target. This is the "reconcile-
while-stopped is a dry-run; next start applies naturally" contract
from the spec.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_volume_added_while_stopped_picked_up_on_next_shell(devm, workspace, sandbox_name):
    workspace.write_devmyaml(install=["true"])
    try:
        # Cold-start without a volume.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"first cold-start failed:\n{r.stderr.decode()}"

        # Stop, preserving disk.
        subprocess.run(
            [devm.path, "stop", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )

        # Add the volume declaration while stopped.
        devm.unlock()
        workspace.patch_devmyaml(
            install=["true"],
            volumes={"scratch": "/var/lib/scratch"},
        )

        # Cold-start again — the new volume must appear at the target
        # (empty in this case, since neither side had content).
        r = subprocess.run(
            [devm.path, "shell", "--", "sudo", "sh", "-c",
             "mountpoint -q /var/lib/scratch && echo mounted || echo NOT_MOUNTED"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"post-restart shell failed:\n{r.stderr.decode()}"
        assert "mounted" in r.stdout.decode(), (
            f"volume was not mounted after re-shell:\n{r.stdout.decode()!r}"
        )

        # Prove the mount is really the volume: write, teardown, cold-start,
        # verify the write persists.
        r = subprocess.run(
            [devm.path, "shell", "--", "sudo", "sh", "-c",
             "echo persist > /var/lib/scratch/sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0

        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        r = subprocess.run(
            [devm.path, "shell", "--", "cat", "/var/lib/scratch/sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0
        assert r.stdout.decode().strip() == "persist"
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
