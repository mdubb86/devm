"""Propose channel: a guest-side `propose` call signals a devm.yaml
edit to the Mac side, the approve gate fires on the next reconcile,
`devm approve` records attribution and clears it, and reconcile then
proceeds.

The guest binary sends attribution only -- cwd, branch, reason, kind,
source -- never the config bytes themselves. Those reach the daemon
exclusively through the project's dedicated config-sync mutagen
session (internal/serviceapi/config_sync.go), so this test edits
devm.yaml on the Mac side directly and waits for the sync tick to land
it in the guest before signaling propose from there.

Exercises the full path: internal/serviceapi/config_sync.go's
bidirectional sync -> cmd/propose (guest binary) -> softnet
192.168.127.1:82 -> internal/serviceapi's per-project /propose
listener -> WriteLastProposal -> the approve-gate refusal in
internal/serviceapi/approve.go -> `devm approve` -> ClearLastProposal.
"""
from __future__ import annotations

import json
import subprocess
import time
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_propose_channel(workspace, devm, sandbox_name):
    workspace.write_devmyaml()  # default repos.main -> Hello-World, packages: [git]
    try:
        cold = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert cold.returncode == 0, f"start failed: {cold.stderr.decode()!r}"

        # Mac-side edit -- a LIVE-bucket env var, so once approved,
        # reconcile can apply it without a recreate.
        workspace.patch_devmyaml(env={"DEVM_PROPOSE_E2E": "1"})

        # Wait for the config-sync mutagen session to land the edit in
        # the guest before signaling propose from there.
        deadline = time.monotonic() + 30
        guest_content = ""
        while time.monotonic() < deadline:
            cat = subprocess.run(
                [devm.path, "exec", "cat", "/home/devm/devm.yaml"],
                cwd=str(workspace.path), capture_output=True, timeout=15,
            )
            guest_content = cat.stdout.decode()
            if "DEVM_PROPOSE_E2E" in guest_content:
                break
            time.sleep(1)
        assert "DEVM_PROPOSE_E2E" in guest_content, (
            f"guest never saw the Mac-side edit:\n{guest_content}"
        )

        # Guest-side propose: signal only, no config bytes in the call.
        propose = subprocess.run(
            [devm.path, "exec", "/opt/devm/bin/propose", "--reason", "add env var"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert propose.returncode == 0, (
            f"propose failed (rc={propose.returncode}):\n"
            f"stdout: {propose.stdout.decode()!r}\n"
            f"stderr: {propose.stderr.decode()!r}"
        )

        # last-proposal.json carries the guest's attribution.
        meta_path = (
            Path.home() / "Library" / "Application Support" / "devm-e2e"
            / sandbox_name / "last-proposal.json"
        )
        assert meta_path.exists(), f"no last-proposal.json at {meta_path}"
        meta = json.loads(meta_path.read_text())
        assert meta["source"] == "guest", meta
        assert meta["reason"] == "add env var", meta

        # The approve gate fires on the next reconcile -- --yes does NOT
        # bypass it (same contract test_244 pins for a Mac-side edit).
        refuse = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert refuse.returncode != 0, "reconcile must refuse on an unapproved guest proposal"
        stderr = refuse.stderr.decode()
        assert "changed since it was last approved" in stderr, stderr
        assert "devm approve" in stderr, stderr

        # `devm approve` advances the approved snapshot and clears the
        # proposal metadata.
        approve = subprocess.run(
            [devm.path, "approve"],
            cwd=str(workspace.path), input=b"y\n",
            capture_output=True, timeout=30,
        )
        assert approve.returncode == 0, f"approve failed: {approve.stderr.decode()!r}"
        assert not meta_path.exists(), "last-proposal.json should be cleared after approve"

        # Reconcile now proceeds -- an env change is a LIVE-bucket edit,
        # applied without a recreate.
        rec = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=90,
        )
        assert rec.returncode == 0, f"reconcile after approve failed: {rec.stderr.decode()!r}"
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
