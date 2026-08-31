"""144: `devm purge --yes` deletes a project dir with no VM + no state.

The opposite of test_143. Cold-start, write to a volume, teardown the
VM, then also delete the state file so the project is fully absent
from devm's world. Run purge and verify the volume dir is gone.
"""
from __future__ import annotations

import os
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_purge_removes_orphaned_volumes(devm, workspace, sandbox_name):
    workspace.write_devmyaml(
        install=["true"],
        volumes={"scratch": "/var/lib/scratch"},
    )
    home = os.path.expanduser("~")
    mac_vol_dir = workspace.volume_path("scratch").parent
    try:
        # Cold-start, seed the volume with content.
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        r = subprocess.run(
            [devm.path, "shell", "--", "sudo", "sh", "-c",
             "echo orphan > /var/lib/scratch/sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"sentinel write failed:\n{r.stderr.decode()}"
        assert os.path.exists(mac_vol_dir), "Mac-side volume dir must exist after cold-start"

        # Teardown destroys the VM disk; volume dir remains.
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert os.path.exists(mac_vol_dir), "Mac-side volume dir must survive teardown"

        # Also delete the state file to simulate "project fully gone."
        state_path = os.path.join(
            home, "Library", "Application Support", "devm-e2e",
            "state", f"{workspace.slug}.json",
        )
        if os.path.exists(state_path):
            os.remove(state_path)

        # Now purge should find and delete it.
        r = subprocess.run(
            [devm.path, "purge", "--yes"],
            cwd=os.path.expanduser("~"), capture_output=True, timeout=30,
        )
        assert r.returncode == 0
        out = r.stdout.decode()
        assert f"deleted '{workspace.slug}'" in out, (
            f"purge output missing delete confirmation:\n{out}"
        )
        assert not os.path.exists(mac_vol_dir), "volume dir still exists after purge"
    finally:
        # Nothing to teardown (already torn down); best-effort mac dir wipe.
        import shutil
        shutil.rmtree(mac_vol_dir, ignore_errors=True)
