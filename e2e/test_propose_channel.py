"""Propose channel: a guest-side `propose` call writes devm.yaml on the
Mac side, the approve gate fires on the next reconcile, `devm approve`
records attribution and clears it, and reconcile then proceeds.

Exercises the full path: cmd/propose (guest binary) -> softnet
192.168.127.1:82 -> internal/serviceapi's per-project /propose
listener -> WriteLastProposal -> the approve-gate refusal in
internal/serviceapi/approve.go -> `devm approve` -> ClearLastProposal.
"""
from __future__ import annotations

import base64
import json
import subprocess
from pathlib import Path

import pytest
import yaml

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

        # Build a modified devm.yaml (add a trivial env var -- a LIVE-bucket
        # change reconcile can apply without a recreate, so this test isn't
        # coupled to guest apt/network plumbing) and ship it from the guest
        # via `propose`, exactly as a guest-side agent would.
        cfg = yaml.safe_load(workspace.devmyaml_path.read_text())
        cfg.setdefault("env", {})["DEVM_PROPOSE_E2E"] = "1"
        new_content = yaml.safe_dump(cfg, sort_keys=False)
        encoded = base64.b64encode(new_content.encode()).decode()

        propose = subprocess.run(
            [devm.path, "shell", "--", "bash", "-c",
             f"echo {encoded} | base64 -d > /tmp/new-devm.yaml && "
             f"/opt/devm/bin/propose --reason 'add env var' /tmp/new-devm.yaml"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert propose.returncode == 0, (
            f"propose failed (rc={propose.returncode}):\n"
            f"stdout: {propose.stdout.decode()!r}\n"
            f"stderr: {propose.stderr.decode()!r}"
        )

        # Mac side sees the proposed content, byte for byte.
        assert workspace.devmyaml_path.read_text() == new_content

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

        # last-proposal.json carries the guest's attribution.
        meta_path = (
            Path.home() / "Library" / "Application Support" / "devm-e2e"
            / sandbox_name / "last-proposal.json"
        )
        assert meta_path.exists(), f"no last-proposal.json at {meta_path}"
        meta = json.loads(meta_path.read_text())
        assert meta["source"] == "guest", meta
        assert meta["reason"] == "add env var", meta

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
