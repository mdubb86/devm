"""151: live-add mask on a workspace path that already has content.

Cold-start WITHOUT the mask, populate <workspace>/node_modules with
content (via Mac side — devm shell writes are already virtiofs-
mediated to Mac), live-add the mask via reconcile, verify:

  - Reconcile classifies the change as Live (no restart prompt).
  - Post-reconcile: <workspace>/node_modules appears empty in the
    guest (the bind mount shadowed the Mac-side content).
  - Mac-side content is still there (bind hides but doesn't delete).
  - Writing new content into the workspace target lands on guest
    ext4 (visible at /var/devm/masks/<project>/node_modules/) but
    NOT on the Mac side.

This is the primary use case for masks: platform-content isolation
without disrupting Mac-side state.
"""
from __future__ import annotations

import os
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_mask_live_add_on_populated(devm, workspace, sandbox_name):
    workspace.write_devmyaml(install=["true"])
    mac_dir = os.path.join(str(workspace.path), "node_modules")
    os.makedirs(mac_dir, exist_ok=True)
    with open(os.path.join(mac_dir, "mac-only"), "w") as f:
        f.write("original mac content\n")

    try:
        # Cold-start (no mask yet).
        r = subprocess.run(
            [devm.path, "shell", "--", "cat",
             f"{workspace.path}/node_modules/mac-only"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"pre-mask read failed:\n{r.stderr.decode()}"
        assert r.stdout.decode().strip() == "original mac content"

        # Live-add the mask via reconcile.
        devm.unlock()
        workspace.patch_devmyaml(install=["true"], masks=["node_modules"])
        r = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"reconcile failed:\n{r.stderr.decode()}"
        # No restart prompt = mask change was BucketLive.

        # Guest now sees an empty node_modules (mask shadowed).
        r = subprocess.run(
            [devm.path, "shell", "--", "ls", "-A",
             f"{workspace.path}/node_modules"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, f"post-mask ls failed:\n{r.stderr.decode()}"
        assert r.stdout.decode().strip() == "", (
            f"expected empty node_modules post-mask, got: {r.stdout.decode()!r}"
        )

        # Mac side still has the original file (hidden by bind, not deleted).
        assert os.path.exists(os.path.join(mac_dir, "mac-only"))

        # Write into the workspace path — lands on guest ext4, not Mac.
        r = subprocess.run(
            [devm.path, "shell", "--", "sh", "-c",
             f"echo linux-content > {workspace.path}/node_modules/linux-only"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0

        # Verify it's at the storage path.
        r = subprocess.run(
            [devm.path, "shell", "--", "cat",
             f"/var/devm/masks/{workspace.slug}/node_modules/linux-only"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0
        assert r.stdout.decode().strip() == "linux-content"

        # And NOT on Mac side.
        assert not os.path.exists(os.path.join(mac_dir, "linux-only"))
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        # Cleanup: remove the pre-seed dir we created on Mac.
        import shutil
        shutil.rmtree(mac_dir, ignore_errors=True)
