"""The guest-side `gdevm propose` binary sends attribution only -- no
config bytes. cmd/gdevm/propose.go's doPost posts {cwd, branch,
reason, kind, source: "guest"} to softnet 192.168.127.1:82; the config
bytes it might have touched reach the Mac side (if at all) only
through the separate config-sync mutagen session, never through the
propose call itself.

Proves this by editing devm.yaml on the Mac side, capturing its bytes,
running the guest propose binary, and asserting those bytes are
untouched afterward -- if propose shipped a body, it would have
clobbered the edit.

Same scenario also folds in coverage for two adjacent invariants on
the same VM (no extra cold-start cost):
  - a second `gdevm propose` with no intervening edit short-circuits at
    the daemon: exit 0, last-proposal.json still holds the FIRST
    proposal's attribution (never overwritten by a no-change signal).
  - flipping `guest.propose: false` in devm.yaml makes the daemon
    refuse subsequent guest-source signals: non-zero exit and
    "guest.propose is disabled" on stderr.
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
        before = workspace.devm_yaml_path.read_text()

        guest = subprocess.run(
            [devm.path, "exec", "gdevm", "propose", "--reason", "add y"],
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
        assert workspace.devm_yaml_path.read_text() == before

        # ---- No-change short-circuit: a second propose call with no
        # ---- intervening edit exits 0 and does NOT overwrite the
        # ---- attribution already on record.
        first_meta_bytes = meta_path.read_bytes()
        second = subprocess.run(
            [devm.path, "exec", "gdevm", "propose", "--reason", "same"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert second.returncode == 0, (
            f"no-change propose must exit 0 (rc={second.returncode}):\n"
            f"stdout: {second.stdout.decode()!r}\n"
            f"stderr: {second.stderr.decode()!r}"
        )
        assert meta_path.read_bytes() == first_meta_bytes, (
            "no-change propose must not overwrite last-proposal.json "
            "(daemon short-circuits before WriteLastProposal)"
        )

        # ---- Gate off: flipping guest.propose:false in devm.yaml
        # ---- makes subsequent guest-source signals hard-fail with
        # ---- the daemon's own refusal on stderr.
        workspace.patch_devmyaml(guest={"propose": False})
        gated = subprocess.run(
            [devm.path, "exec", "gdevm", "propose", "--reason", "x"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert gated.returncode != 0, (
            f"gated propose must fail; got rc={gated.returncode}\n"
            f"stdout: {gated.stdout.decode()!r}\n"
            f"stderr: {gated.stderr.decode()!r}"
        )
        assert "guest.propose is disabled" in gated.stderr.decode(), (
            f"gated propose stderr must name the disabled gate; got:\n"
            f"{gated.stderr.decode()!r}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
