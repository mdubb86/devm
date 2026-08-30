"""Prove sync works even when guest sshd is dead. Confirms that mutagen's
transport doesn't touch sshd at all.
"""
from __future__ import annotations
import subprocess
import pytest
from helpers.mutagen_e2e import mirror_path, session_prefix, sync_flush, sync_list

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_sync_works_without_sshd(devm, workspace):
    workspace.write_devmyaml()
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=180)
    assert r.returncode == 0, r.stderr.decode()
    label = workspace.bare_repo_label()

    # Stop sshd inside the guest.
    r = subprocess.run(
        [devm.path, "shell", "--", "sudo", "systemctl", "stop", "ssh"],
        cwd=str(workspace.path), capture_output=True, timeout=60,
    )
    assert r.returncode == 0, r.stderr.decode()

    # Write a file on Mac side, verify it syncs into the guest.
    mirror = mirror_path(workspace.vm_name, label)
    (mirror / "post-sshd-stop.txt").write_text("hello")

    sessions = sync_list(session_prefix(workspace.vm_name))
    sync_flush(sessions[0]["identifier"])

    r = subprocess.run(
        [devm.path, "shell", "--", "cat", f"/home/devm/{label}/post-sshd-stop.txt"],
        cwd=str(workspace.path), capture_output=True, timeout=60,
    )
    assert r.returncode == 0, r.stderr.decode()
    assert "hello" in r.stdout.decode()
