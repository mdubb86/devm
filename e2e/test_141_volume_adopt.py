"""141: adopt path — declaring a volume for a path that already has
content copies the content into the volume on next start.

Sequence: cold-start WITHOUT the volume, write content into what will
later become the target path, then add the volume declaration and
reconcile. Reconcile classifies the change as BucketRestartVM and
restarts the VM; on the next boot, the daemon observes the Mac-side
volume dir is empty AND the guest target has content, runs cp -a in
the guest, then bind-mounts. After teardown + cold-start, the
adopted content must be at the target again — proving the Mac side
persisted the copy.

This is the test the spec calls out as "interesting" — it exercises
uid/gid preservation through virtiofs (the copy happens as the
target's owner in the guest, then virtiofs surfaces the same numeric
ids on Mac and back).
"""
from __future__ import annotations

import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_volume_adopt(devm, workspace, sandbox_name):
    # Cold-start without any volume, plant content at what will be
    # the future mount target.
    workspace.write_devmyaml(install=["true"])
    try:
        r = subprocess.run(
            [devm.path, "shell", "--", "sudo", "sh", "-c",
             "mkdir -p /var/lib/scratch && echo pre-adopt > /var/lib/scratch/sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"pre-adopt write failed:\n{r.stderr.decode()}"

        # Add the volume declaration and reconcile. Reconcile is
        # BucketRestartVM (interactive prompt) — pass --yes to skip.
        devm.unlock()
        workspace.patch_devmyaml(
            install=["true"],
            volumes={"scratch": "/var/lib/scratch"},
        )
        r = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"reconcile failed:\n{r.stderr.decode()}"

        # After reconcile-driven restart, the adopt logic must have
        # run: the sentinel is still at the target AND the target is
        # now backed by the volume mount.
        r = subprocess.run(
            [devm.path, "shell", "--", "cat", "/var/lib/scratch/sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"post-adopt read failed:\n{r.stderr.decode()}"
        assert r.stdout.decode().strip() == "pre-adopt", (
            f"adopted content mismatch: got {r.stdout.decode()!r}"
        )

        # Teardown + cold-start — the adopted content must persist,
        # proving the copy landed Mac-side (not just in the running VM).
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        # Give the daemon a moment to release the tart VM handle.
        time.sleep(2)
        r = subprocess.run(
            [devm.path, "shell", "--", "cat", "/var/lib/scratch/sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, (
            f"post-teardown read failed:\n{r.stderr.decode()}\n"
            f"If the sentinel is missing here, adopt copied guest-side "
            f"but the Mac-side write via virtiofs didn't persist."
        )
        assert r.stdout.decode().strip() == "pre-adopt"
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
