"""142: both-non-empty conflict on volume mount → clear error.

Pre-seeds both sides — Mac-side volume dir AND the guest target — with
different content, then cold-starts. The daemon's guest mount script
must detect the conflict, emit the spec's exact conflict message
(names both remediation paths), and fail non-zero without binding
the volume over the target.

Fail conditions:
  - VM comes up successfully with the volume bound (silent
    overwrite is exactly what adopt is supposed to prevent).
  - Error message is missing either remediation path or the volume
    name.
"""
from __future__ import annotations

import os
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(900)
@pytest.mark.slow
def test_volume_conflict_errors_clearly(devm, workspace, sandbox_name):
    home = os.path.expanduser("~")
    mac_dir = os.path.join(
        home, "Library", "Application Support", "devm-e2e",
        "volumes", workspace.slug, "scratch",
    )
    try:
        # Phase 1: cold-start WITHOUT the volume, seed the guest target
        # with content, then teardown. Teardown wipes the VM disk but the
        # Mac-side volume dir doesn't exist yet — nothing to preserve.
        workspace.write_devmyaml(
            install=[
                "sudo mkdir -p /var/lib/scratch && sudo sh -c 'echo guest > /var/lib/scratch/guest-side'",
            ],
        )
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"phase 1 cold-start failed:\n{r.stderr.decode()}"
        # Keep the VM disk (don't teardown); we need the guest target
        # to still have content for phase 2. `devm stop` preserves disk.
        subprocess.run(
            [devm.path, "stop", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )

        # Phase 2: pre-seed the Mac-side volume dir with different
        # content, add the volume declaration, then cold-start (which
        # is a warm start because the VM disk is still there).
        os.makedirs(mac_dir, mode=0o700, exist_ok=True)
        with open(os.path.join(mac_dir, "mac-side"), "w") as f:
            f.write("mac content\n")
        devm.unlock()
        workspace.patch_devmyaml(
            install=[
                "sudo mkdir -p /var/lib/scratch && sudo sh -c 'echo guest > /var/lib/scratch/guest-side'",
            ],
            volumes={"scratch": "/var/lib/scratch"},
        )
        # Attempt to bring the sandbox up. The volume mount script must
        # fail loudly, exiting non-zero before the shell attaches.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode != 0, (
            "start unexpectedly succeeded — the daemon must fail loudly "
            "when both Mac volume and guest target have content."
        )
        stderr = r.stderr.decode()
        assert "mount conflict" in stderr, f"missing conflict header:\n{stderr}"
        assert "scratch" in stderr, f"missing volume name:\n{stderr}"
        assert "/var/lib/scratch" in stderr, f"missing guest path:\n{stderr}"
        assert "clear guest content" in stderr, f"missing guest-remediation line:\n{stderr}"
        assert "clear Mac volume" in stderr, f"missing Mac-remediation line:\n{stderr}"
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        import shutil
        shutil.rmtree(mac_dir, ignore_errors=True)
