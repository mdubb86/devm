"""248: create devm.me.yaml on the guest for the first time, propose from
there, approve on the Mac, and prove the whole round-trip works.

Pins the shape shelfmates hit: a config file that didn't exist at
cold-start shows up in the guest, `gdevm propose` names it as a
changed file (Kinds includes "devm.me.yaml"), `devm approve` accepts
it, and a follow-up reconcile no longer refuses.

The propose flow no longer takes --kind — it scans every proposable
file (devm.yaml, devm.me.yaml, devm.sh, devm.me.sh) against the
last-approved snapshot and records every one that diverged. Before
this scan-all model, editing a file other than devm.yaml (the flag
default) silently reported "no changes since last approval". This
test locks the fix end-to-end.
"""
from __future__ import annotations

import json
import subprocess
import time
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(420)
def test_new_me_yaml_from_guest_flows_to_approved_snapshot(workspace, devm, sandbox_name):
    workspace.write_devmyaml(no_repo=True)
    try:
        cold = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=240,
        )
        assert cold.returncode == 0, f"cold-start failed: {cold.stderr.decode()!r}"

        # No devm.me.yaml on either side to begin with.
        assert not (workspace.path / "devm.me.yaml").exists(), \
            "test precondition: no devm.me.yaml on the Mac side before the guest creates it"

        # Guest creates devm.me.yaml at /home/devm/devm.me.yaml. The
        # config-sync mutagen session ships it to the Mac at
        # <macCwd>/devm.me.yaml — same session that carries devm.yaml
        # in both directions (see internal/serviceapi/config_sync.go's
        # configSyncIgnores allow-list).
        guest_me_body = 'env:\n  GUEST_ADDED_ME: "1"\n'
        create = subprocess.run(
            [devm.path, "exec", "bash", "-c",
             f"printf %s {json.dumps(guest_me_body)} > /home/devm/devm.me.yaml"],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert create.returncode == 0, create.stderr.decode()

        # Wait for the Mac side to see the new file. config-sync runs
        # roughly a few seconds behind guest edits under load; a bounded
        # poll avoids a fixed sleep that would either flake short or
        # waste time.
        mac_me_path = workspace.path / "devm.me.yaml"
        deadline = time.monotonic() + 45
        while time.monotonic() < deadline:
            if mac_me_path.exists() and mac_me_path.read_text() == guest_me_body:
                break
            time.sleep(1)
        assert mac_me_path.exists(), \
            f"config-sync never delivered devm.me.yaml to the Mac at {mac_me_path}"
        assert mac_me_path.read_text() == guest_me_body, \
            f"Mac-side devm.me.yaml content mismatch:\n{mac_me_path.read_text()!r}"

        # Guest proposes. The daemon scans every proposable file at
        # macCwd against the approved snapshot; devm.me.yaml is new so
        # it must appear in Kinds. No --kind flag is passed (and none
        # exists any more) — the whole point of the scan-all model.
        propose = subprocess.run(
            [devm.path, "exec", "gdevm", "propose", "--reason", "add me override"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert propose.returncode == 0, (
            f"gdevm propose failed (rc={propose.returncode}):\n"
            f"stdout: {propose.stdout.decode()!r}\n"
            f"stderr: {propose.stderr.decode()!r}"
        )
        assert b"no changes since last approval" not in propose.stdout, (
            "propose must see devm.me.yaml as a change, not report a no-op; "
            f"stdout: {propose.stdout.decode()!r}"
        )

        meta_path = (
            Path.home() / "Library" / "Application Support" / "devm-e2e"
            / sandbox_name / "last-proposal.json"
        )
        assert meta_path.exists(), f"no last-proposal.json at {meta_path}"
        meta = json.loads(meta_path.read_text())
        assert meta["source"] == "guest", meta
        assert meta["reason"] == "add me override", meta
        assert "devm.me.yaml" in meta.get("kinds", []), (
            f"proposal.kinds must include devm.me.yaml — got {meta.get('kinds')!r}"
        )

        # Reconcile refuses while devm.me.yaml diverges from the
        # (still-empty-for-me.yaml) approved snapshot — proves the new
        # file actually trips the gate, not just the propose recorder.
        refuse = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert refuse.returncode != 0, (
            f"reconcile must refuse when devm.me.yaml diverges; got rc={refuse.returncode}\n"
            f"stderr: {refuse.stderr.decode()!r}"
        )

        # Approve. `devm approve` snapshots all four files at macCwd,
        # so devm.me.yaml lands in the approved-snapshot dir alongside
        # whatever devm.yaml/devm.sh/devm.me.sh look like right now.
        approve = subprocess.run(
            [devm.path, "approve"],
            cwd=str(workspace.path), input=b"y\n",
            capture_output=True, timeout=30,
        )
        assert approve.returncode == 0, f"approve failed: {approve.stderr.decode()!r}"

        # last-proposal.json is cleared on successful approve.
        assert not meta_path.exists(), \
            "successful approve must remove last-proposal.json"

        # Approved snapshot on disk now holds the guest-authored bytes.
        # Snapshot lives at <RuntimeDir>/<project>/approved-snapshot/.
        snapshot_me = (
            Path.home() / "Library" / "Application Support" / "devm-e2e"
            / sandbox_name / "approved-snapshot" / "devm.me.yaml"
        )
        assert snapshot_me.exists(), \
            f"approved snapshot must include devm.me.yaml at {snapshot_me}"
        assert snapshot_me.read_text() == guest_me_body, (
            f"snapshot devm.me.yaml doesn't match guest bytes:\n"
            f"want: {guest_me_body!r}\n"
            f"got:  {snapshot_me.read_text()!r}"
        )

        # Reconcile no longer refuses — the approve gate is clear.
        rec = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=120,
        )
        assert rec.returncode == 0, f"reconcile after approve failed: {rec.stderr.decode()!r}"

        # And a second propose with no further edits short-circuits to
        # "no changes since last approval" — the just-approved bytes
        # are what's on disk, so there's nothing to review.
        again = subprocess.run(
            [devm.path, "exec", "gdevm", "propose", "--reason", "no-op"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert again.returncode == 0, again.stderr.decode()
        assert b"no changes since last approval" in again.stdout, (
            f"post-approve propose with no edits must short-circuit;\n"
            f"stdout: {again.stdout.decode()!r}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
