"""`devm propose --reason ...` run from the Mac side records
attribution with source "mac" -- exercises cmd/devm/propose.go's
runMacProposeWithClient POSTing to the daemon's Unix-socket
/vm/propose?project=<name> handler.

No VM needed: recordProposal (internal/serviceapi/propose.go) reads
the on-disk devm.yaml straight from the project's state dir and writes
last-proposal.json; it never touches the sandbox.
"""
from __future__ import annotations

import json
import subprocess
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(30)
def test_mac_side_propose_records_metadata(workspace, devm, sandbox_name):
    workspace.write_devmyaml(no_repo=True)

    result = subprocess.run(
        [devm.path, "propose", "--reason", "add x"],
        cwd=str(workspace.path), capture_output=True, text=True, timeout=15,
    )
    assert result.returncode == 0, result.stderr

    meta_path = (
        Path.home() / "Library" / "Application Support" / "devm-e2e"
        / sandbox_name / "last-proposal.json"
    )
    assert meta_path.exists(), f"no last-proposal.json at {meta_path}"
    meta = json.loads(meta_path.read_text())
    assert meta["source"] == "mac", meta
    assert meta["reason"] == "add x", meta
