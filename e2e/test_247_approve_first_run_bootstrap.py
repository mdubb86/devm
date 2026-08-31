"""243: on a fresh project (no prior approved snapshot), the first `devm start`
proceeds without prompting and without printing an approve-required error.
Follow-up: a subsequent reconcile with no edits is a no-op that does not
refuse (the snapshot matches).
"""
from __future__ import annotations
import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_first_run_bootstraps_snapshot_silently(workspace, devm, sandbox_name):
    workspace.write_devmyaml(no_repo=True)
    try:
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"first-run start failed: {r.stderr.decode()!r}"
        assert b"approve required" not in r.stderr, "first-run start must not refuse"

        # Reconcile without edits is a no-op that does not refuse.
        rec = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert rec.returncode == 0, f"reconcile after first-run failed: {rec.stderr.decode()!r}"
        assert b"approve required" not in rec.stderr
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path), capture_output=True, timeout=60)
