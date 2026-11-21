"""311: declarative path-mode snapshot on root-owned data with pre/post
service-bracket hooks.

Declares a single `svcdata` snapshot entry with
`path: /var/lib/svcdata` (fully declarative — no `backup_svcdata` /
`restore_svcdata`) and pairs it with the four lifecycle hooks
pre_backup_svcdata / post_backup_svcdata / pre_restore_svcdata /
post_restore_svcdata. The hooks don't bounce a real service; they drop
marker files under /tmp so the test can assert the capture/restore
bracket fired around the tar pipeline.

Flow:
  1. Cold-start with `snapshots.svcdata.path: /var/lib/svcdata`.
  2. Seed `/var/lib/svcdata/blob` as root:root 0640 with known content.
  3. `devm snapshot create svcdata` — captures via the TarRunner.
     Assert archive exists under the e2e RuntimeDir and that
     /tmp/pre_backup_called + /tmp/post_backup_called were touched.
  4. `sudo rm -f /var/lib/svcdata/blob` — simulate data loss.
  5. `devm snapshot restore svcdata` — restores via the TarRunner.
     Assert /tmp/pre_restore_called + /tmp/post_restore_called were
     touched, and the blob is back at root:root 0640 with content DATA.

Pins that path mode preserves root uid/gid and mode on round-trip
through tar + zstd, and that the pre/post hook contract fires around
a declarative path-mode operation.
"""
from __future__ import annotations

import glob
import subprocess
from pathlib import Path

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


_DEVM_SH = """#!/usr/bin/env bash
set -eo pipefail
pre_backup_svcdata()   { touch /tmp/pre_backup_called; }
post_backup_svcdata()  { touch /tmp/post_backup_called; }
pre_restore_svcdata()  { touch /tmp/pre_restore_called; }
post_restore_svcdata() { touch /tmp/post_restore_called; }
"""


def _snapshot_dir(workspace, entry: str) -> Path:
    """Mac-side archive directory for one snapshot entry under the
    devm-e2e identity's RuntimeDir."""
    runtime_dir = Path.home() / "Library" / "Application Support" / "devm-e2e"
    return runtime_dir / workspace.vm_name / "snapshots" / entry


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_snapshot_path_mode_root_pre_post(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        no_repo=True,
        snapshots={
            "svcdata": {"path": "/var/lib/svcdata"},
        },
    )
    workspace.write_devm_sh(_DEVM_SH)

    sandbox = TartSandbox(name=sandbox_name)

    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=600,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    # Wipe any stale marker files so the hook-fire assertions prove the
    # current run's hooks did the touching.
    r = sandbox.exec_shell(
        "rm -f /tmp/pre_backup_called /tmp/post_backup_called "
        "/tmp/pre_restore_called /tmp/post_restore_called"
    )
    assert r.exit_code == 0, f"marker cleanup failed: {r.stderr}"

    # Seed /var/lib/svcdata/blob as root:root 0640 with known content.
    r = sandbox.exec_shell(
        "sudo install -d -m 0755 -o root -g root /var/lib/svcdata && "
        "echo DATA | sudo tee /var/lib/svcdata/blob > /dev/null && "
        "sudo chown root:root /var/lib/svcdata/blob && "
        "sudo chmod 0640 /var/lib/svcdata/blob"
    )
    assert r.exit_code == 0, f"seed svcdata failed: {r.stderr}"

    # Capture — pre_backup_svcdata, TarRunner, post_backup_svcdata.
    devm.snapshot("create", "svcdata", timeout=180)

    # Archive landed on the Mac side.
    matches = glob.glob(str(_snapshot_dir(workspace, "svcdata") / "*.zst"))
    assert len(matches) >= 1, (
        f"expected at least one .zst archive for svcdata under "
        f"{_snapshot_dir(workspace, 'svcdata')}, got {matches}"
    )

    # Pre/post backup hooks fired.
    r = sandbox.exec_shell("test -f /tmp/pre_backup_called")
    assert r.exit_code == 0, "pre_backup_svcdata hook did not fire"
    r = sandbox.exec_shell("test -f /tmp/post_backup_called")
    assert r.exit_code == 0, "post_backup_svcdata hook did not fire"

    # Simulate data loss, then wipe markers again so the restore-phase
    # assertions prove the restore hooks fired (not stale from capture).
    r = sandbox.exec_shell(
        "sudo rm -f /var/lib/svcdata/blob && "
        "rm -f /tmp/pre_restore_called /tmp/post_restore_called"
    )
    assert r.exit_code == 0, f"pre-restore cleanup failed: {r.stderr}"

    # Confirm the blob is actually gone — a no-op restore would pass
    # the ownership/mode/content checks vacuously if we skipped this.
    r = sandbox.exec_shell("sudo test -f /var/lib/svcdata/blob")
    assert r.exit_code != 0, "blob did not get removed before restore"

    # Restore — pre_restore_svcdata, TarRunner, post_restore_svcdata.
    devm.snapshot("restore", "svcdata", timeout=180)

    # Pre/post restore hooks fired.
    r = sandbox.exec_shell("test -f /tmp/pre_restore_called")
    assert r.exit_code == 0, "pre_restore_svcdata hook did not fire"
    r = sandbox.exec_shell("test -f /tmp/post_restore_called")
    assert r.exit_code == 0, "post_restore_svcdata hook did not fire"

    # Blob survived round-trip: root:root 0640 with content DATA.
    r = sandbox.exec_shell("sudo stat -c '%U:%G' /var/lib/svcdata/blob")
    assert r.exit_code == 0, f"stat owner failed: {r.stderr}"
    assert r.stdout.strip() == "root:root", (
        f"blob owner wrong after restore: got {r.stdout!r}"
    )

    r = sandbox.exec_shell("sudo stat -c '%a' /var/lib/svcdata/blob")
    assert r.exit_code == 0, f"stat mode failed: {r.stderr}"
    assert r.stdout.strip() == "640", (
        f"blob mode wrong after restore: got {r.stdout!r}"
    )

    r = sandbox.exec_shell("sudo cat /var/lib/svcdata/blob")
    assert r.exit_code == 0, f"cat blob failed: {r.stderr}"
    assert r.stdout.strip() == "DATA", (
        f"blob content wrong after restore: got {r.stdout!r}"
    )
