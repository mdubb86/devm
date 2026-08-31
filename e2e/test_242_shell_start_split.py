"""242: `devm shell` no longer starts the VM; the split between shell (warm-
attach only) and start (sole cold-start owner) is enforced.
"""
from __future__ import annotations
import subprocess
import pytest
from helpers import stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_shell_errors_on_stopped_vm_and_names_devm_start(workspace, devm, sandbox_name):
    workspace.write_devmyaml(no_repo=True)
    try:
        # Fresh project, no VM has started yet.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode != 0, "shell on stopped VM must error"
        assert "sandbox not running" in r.stderr.decode()
        assert "devm start" in r.stderr.decode()

        # `devm start` succeeds and provisions.
        s = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert s.returncode == 0

        # Now `devm shell -- true` warm-attaches without provisioning.
        w = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert w.returncode == 0, f"warm-attach failed: {w.stderr.decode()!r}"
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path), capture_output=True, timeout=60)
