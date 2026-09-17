"""The guest-side `/opt/devm/bin/propose` binary sends attribution
only -- no config bytes. cmd/propose/main.go's doPost posts
{cwd, branch, reason, kind, source: "guest"} to softnet
192.168.127.1:82; the config bytes it might have touched reach the
Mac side (if at all) only through the separate config-sync mutagen
session, never through the propose call itself.

Proves this by editing devm.yaml on the Mac side, capturing its bytes,
running the guest propose binary, and asserting those bytes are
untouched afterward -- if propose shipped a body, it would have
clobbered the edit.
"""
from __future__ import annotations

import json
import subprocess
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(300)
def test_guest_propose_records_no_bytes(workspace, devm, sandbox_name):
    workspace.write_devmyaml(no_repo=True)
    try:
        cold = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=180,
        )
        assert cold.returncode == 0, f"start failed: {cold.stderr.decode()!r}"

        workspace.patch_devmyaml(env={"GUEST_PROPOSE_E2E": "1"})
        before = workspace.devmyaml_path.read_text()

        guest = subprocess.run(
            [devm.path, "exec", "/opt/devm/bin/propose", "--reason", "add y"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert guest.returncode == 0, (
            f"guest propose failed (rc={guest.returncode}):\n"
            f"stdout: {guest.stdout.decode()!r}\n"
            f"stderr: {guest.stderr.decode()!r}"
        )

        meta_path = (
            Path.home() / "Library" / "Application Support" / "devm-e2e"
            / sandbox_name / "last-proposal.json"
        )
        assert meta_path.exists(), f"no last-proposal.json at {meta_path}"
        meta = json.loads(meta_path.read_text())
        assert meta["source"] == "guest", meta
        assert meta["reason"] == "add y", meta

        # propose sent no config bytes -- the Mac-side file the human
        # edited is exactly what it was before the guest call.
        assert workspace.devmyaml_path.read_text() == before
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
