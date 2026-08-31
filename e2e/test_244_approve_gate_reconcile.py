"""240: approve gate — devm reconcile refuses when devm.yaml diverges from
the last-approved snapshot; `devm approve` unblocks; subsequent reconcile
proceeds.

Pins the CLI contract: --yes on reconcile does NOT bypass the approve
gate; only `devm approve` (interactive, no --yes flag) advances the
snapshot.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_reconcile_refuses_on_divergence_and_approve_unblocks(workspace, devm, sandbox_name):
    workspace.write_devmyaml(no_repo=True)
    try:
        # Cold-start via `devm start`; first-run auto-approves.
        cold = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert cold.returncode == 0, f"start failed: {cold.stderr.decode()!r}"

        # Edit devm.yaml — trivial change that reconcile would apply live.
        workspace.patch_devmyaml(env={"DEVM_APPROVE_E2E": "1"})

        # `devm reconcile --yes` MUST refuse and name both paths.
        refuse = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert refuse.returncode != 0, "reconcile must refuse when devm.yaml diverges"
        stderr = refuse.stderr.decode()
        assert "changed since it was last approved" in stderr, stderr
        assert "devm approve" in stderr, stderr
        assert "menu bar icon" in stderr, stderr

        # `devm approve` with piped `y\n` advances the snapshot.
        approve = subprocess.run(
            [devm.path, "approve"],
            cwd=str(workspace.path), input=b"y\n",
            capture_output=True, timeout=30,
        )
        assert approve.returncode == 0, f"approve failed: {approve.stderr.decode()!r}"
        assert "approved" in approve.stdout.decode()

        # Reconcile now proceeds.
        rec = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=120,
        )
        assert rec.returncode == 0, f"reconcile after approve failed: {rec.stderr.decode()!r}"
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path), capture_output=True, timeout=60)
