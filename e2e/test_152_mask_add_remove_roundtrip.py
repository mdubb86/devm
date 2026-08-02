"""152: live-add mask, write content, live-remove mask.

Verifies:
  - Live-remove works when nothing holds the mount open (no EBUSY).
  - After live-remove, the workspace target reverts to whatever's on
    Mac side (backing content preserved on guest ext4 but no longer
    bound — spec says the storage dir isn't deleted).
  - Re-adding the mask sees the previously-written content still
    there in the storage dir (proof of preserve-on-remove).
"""
from __future__ import annotations

import os
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_mask_add_remove_roundtrip(devm, workspace, sandbox_name):
    # Pre-seed workspace-side content so post-remove has something to
    # revert to.
    workspace.write_devmyaml(install=["true"])
    mac_dir = os.path.join(str(workspace.path), "scratch")
    os.makedirs(mac_dir, exist_ok=True)
    with open(os.path.join(mac_dir, "mac-view"), "w") as f:
        f.write("mac original\n")

    try:
        # Cold-start (no mask).
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0

        # Live-add.
        devm.unlock()
        workspace.patch_devmyaml(install=["true"], masks=["scratch"])
        r = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0

        # Write into the mask.
        r = subprocess.run(
            [devm.path, "shell", "--", "sh", "-c",
             f"echo linux > {workspace.path}/scratch/linux-view"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0

        # Live-remove. Use write_devmyaml (full replace) so the masks
        # key is dropped from the yaml; patch_devmyaml is additive and
        # would leave the previously-set masks in place.
        devm.unlock()
        workspace.write_devmyaml(install=["true"])
        r = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"reconcile-remove failed:\n{r.stderr.decode()}"

        # Post-remove: workspace target reverts to Mac view.
        r = subprocess.run(
            [devm.path, "shell", "--", "cat",
             f"{workspace.path}/scratch/mac-view"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0
        assert r.stdout.decode().strip() == "mac original"

        # The linux-only file we wrote into the mask should NOT be
        # visible on the workspace path (mask is unmounted).
        r = subprocess.run(
            [devm.path, "shell", "--", "test", "-e",
             f"{workspace.path}/scratch/linux-view"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode != 0, "post-remove: mask content leaked into workspace view"

        # BUT it should still be on guest ext4 (preserved for re-attach).
        r = subprocess.run(
            [devm.path, "shell", "--", "cat",
             f"/var/devm/masks/{workspace.slug}/scratch/linux-view"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, "backing content was wiped on remove — spec says preserve"
        assert r.stdout.decode().strip() == "linux"

        # Re-add and verify the linux content comes back.
        devm.unlock()
        workspace.patch_devmyaml(install=["true"], masks=["scratch"])
        r = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0
        r = subprocess.run(
            [devm.path, "shell", "--", "cat",
             f"{workspace.path}/scratch/linux-view"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0
        assert r.stdout.decode().strip() == "linux"
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        import shutil
        shutil.rmtree(mac_dir, ignore_errors=True)
