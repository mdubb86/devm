"""241: approve gate — `devm start` refuses on divergence too; first cold-
start auto-approves silently.
"""
from __future__ import annotations
import subprocess
import pytest
from helpers import stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_start_refuses_on_divergence_after_first_boot(workspace, devm, sandbox_name):
    workspace.write_devmyaml(no_repo=True)
    try:
        # First cold-start: bootstrap silently.
        assert subprocess.run([devm.path, "start"], cwd=str(workspace.path), capture_output=True, timeout=300).returncode == 0
        stop_and_wait_stopped(devm, sandbox_name)

        # Edit devm.yaml while stopped.
        workspace.patch_devmyaml(env={"DEVM_APPROVE_START_E2E": "1"})

        # `devm start` must now refuse.
        refuse = subprocess.run([devm.path, "start"], cwd=str(workspace.path), capture_output=True, timeout=60)
        assert refuse.returncode != 0
        assert "changed since it was last approved" in refuse.stderr.decode()

        # Approve, then start succeeds.
        approve = subprocess.run([devm.path, "approve"], cwd=str(workspace.path), input=b"y\n", capture_output=True, timeout=30)
        assert approve.returncode == 0

        start = subprocess.run([devm.path, "start"], cwd=str(workspace.path), capture_output=True, timeout=300)
        assert start.returncode == 0
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path), capture_output=True, timeout=60)
