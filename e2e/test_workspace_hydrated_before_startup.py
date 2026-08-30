"""Prove workspace is hydrated by the time top-level startup: runs.

startup: reads a file placed via mutagen. Runs on every boot — cold-start
AND `devm stop; devm start` cycle both succeed.
"""
from __future__ import annotations
import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(400)
def test_startup_sees_hydrated_workspace(devm, workspace):
    workspace.write_devmyaml(
        startup=["test -f $WORKSPACE/README"],
    )
    # Cold-start
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=180)
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    # Stop then start again (warm cycle).
    subprocess.run([devm.path, "stop"], cwd=str(workspace.path), timeout=60)
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=180)
    assert r.returncode == 0, f"warm-start failed:\n{r.stderr.decode()}"
