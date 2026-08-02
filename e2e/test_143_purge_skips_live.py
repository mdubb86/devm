"""143: `devm purge --yes` skips a project whose VM is still alive.

The safety property of purge: it must never touch a live project's
volume data. Cold-start a project, run `devm purge --yes` from
outside the project dir (purge is top-level, not cwd-sensitive),
verify the project's volume dir is intact and purge's output names
the skip.
"""
from __future__ import annotations

import os
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_purge_skips_live_projects(devm, workspace, sandbox_name):
    workspace.write_devmyaml(
        install=["true"],
        volumes={"scratch": "/var/lib/scratch"},
    )
    try:
        # Cold-start creates the VM + the Mac-side volume dir.
        r = subprocess.run(
            [devm.path, "shell", "--", "sudo", "sh", "-c",
             "echo alive > /var/lib/scratch/sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # Run purge from OUTSIDE the project dir.
        r = subprocess.run(
            [devm.path, "purge", "--yes"],
            cwd=os.path.expanduser("~"), capture_output=True, timeout=30,
        )
        assert r.returncode == 0
        out = r.stdout.decode()
        assert f"skipped '{workspace.slug}'" in out, (
            f"purge should have skipped live project. Output:\n{out}"
        )

        # Volume dir still there + sentinel intact.
        home = os.path.expanduser("~")
        mac_dir = os.path.join(
            home, "Library", "Application Support", "devm-e2e",
            "volumes", workspace.slug, "scratch",
        )
        assert os.path.exists(mac_dir), f"purge wiped a live project's volume dir!"
        with open(os.path.join(mac_dir, "sentinel")) as f:
            assert f.read().strip() == "alive"
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
