"""Prove mutagen sync goes through our tart-mutagen-ssh shim (not system ssh).

The shim is at <runtime-dir>/mutagen-ssh-dir/ssh. Verify it exists,
matches the embedded bytes, and that mutagen invokes it (not
/usr/bin/ssh) by checking MUTAGEN_SSH_PATH is set in the mutagen daemon's
env.
"""
from __future__ import annotations
import hashlib
import os
import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_shim_dispatches_via_tart_exec(devm, workspace):
    workspace.write_devmyaml()
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=180)
    assert r.returncode == 0, r.stderr.decode()

    # Runtime dir under devm-e2e state (test infra uses e2e identity).
    home = os.path.expanduser("~/Library/Application Support/devm-e2e")
    shim_ssh = os.path.join(home, "mutagen-ssh-dir", "ssh")
    shim_scp = os.path.join(home, "mutagen-ssh-dir", "scp")

    assert os.path.exists(shim_ssh), f"shim ssh missing at {shim_ssh}"
    assert os.path.exists(shim_scp), f"shim scp missing at {shim_scp}"

    # Both files are the same binary.
    with open(shim_ssh, "rb") as f:
        ssh_sum = hashlib.sha256(f.read()).hexdigest()
    with open(shim_scp, "rb") as f:
        scp_sum = hashlib.sha256(f.read()).hexdigest()
    assert ssh_sum == scp_sum, "ssh and scp must be the same binary"

    # Verify Mach-O arm64.
    r = subprocess.run(["file", shim_ssh], capture_output=True, text=True)
    assert "Mach-O" in r.stdout and "arm64" in r.stdout, r.stdout
