"""Regression fence: sshd wasn't accidentally removed from the guest image."""
from __future__ import annotations
import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_sshd_installed(devm, workspace):
    workspace.write_devmyaml()
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=180)
    assert r.returncode == 0, r.stderr.decode()

    r = subprocess.run(
        [devm.path, "shell", "--", "ls", "/etc/ssh/sshd_config"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 0, "sshd_config must exist"

    r = subprocess.run(
        [devm.path, "shell", "--", "systemctl", "is-active", "ssh"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 0 and "active" in r.stdout.decode(), \
        f"sshd must be active: {r.stdout.decode()} / {r.stderr.decode()}"
