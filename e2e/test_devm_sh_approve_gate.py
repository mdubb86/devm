"""devm.sh is hashed into the same approve snapshot as devm.yaml: a
cold-start bootstraps the snapshot with no divergence, but editing
devm.sh afterward trips the approve gate exactly like editing
devm.yaml does, and `devm approve` unblocks it the same way.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_devm_sh_edit_triggers_approve_gate(workspace, devm):
    workspace.write_devmyaml(no_repo=True)
    workspace.write_devm_sh(
        "#!/usr/bin/env bash\n"
        "set -eo pipefail\n"
        "install() {\n"
        "  echo v1\n"
        "}\n"
    )
    try:
        # Cold-start via `devm start`; first-run bootstraps the snapshot,
        # so no divergence is possible yet.
        cold = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=240,
        )
        assert cold.returncode == 0, f"start failed: {cold.stderr.decode()!r}"

        # Edit devm.sh — a function body change, no devm.yaml touch.
        workspace.write_devm_sh(
            "#!/usr/bin/env bash\n"
            "set -eo pipefail\n"
            "install() {\n"
            "  echo v2\n"
            "}\n"
        )

        refuse = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert refuse.returncode != 0, "reconcile must refuse when devm.sh diverges"
        stderr = refuse.stderr.decode()
        assert "approve_required" in stderr or "has changed since it was last approved" in stderr, stderr

        approve = subprocess.run(
            [devm.path, "approve"],
            cwd=str(workspace.path), input=b"y\n",
            capture_output=True, timeout=30,
        )
        assert approve.returncode == 0, f"approve failed: {approve.stderr.decode()!r}"

        rec = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=120,
        )
        assert rec.returncode == 0, f"reconcile after approve failed: {rec.stderr.decode()!r}"
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
