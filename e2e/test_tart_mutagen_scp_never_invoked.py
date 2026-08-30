"""Regression tripwire: if mutagen ever invokes scp (agent transfer), the
shim errors loudly. Prove no invocation across a full cold-start + sync
cycle. This validates that pre-installing the agent bypasses SCP.
"""
from __future__ import annotations
import os
import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_scp_never_invoked(devm, workspace, tmp_path):
    # Overwrite the shim's `scp` copy with a script that WRITES to a log
    # file on any invocation. If mutagen calls scp, we detect it.
    home = os.path.expanduser("~/Library/Application Support/devm-e2e")
    scp_path = os.path.join(home, "mutagen-ssh-dir", "scp")

    # Save original.
    backup = tmp_path / "scp.original"
    with open(scp_path, "rb") as f:
        backup.write_bytes(f.read())

    trap = tmp_path / "scp-invoked.log"
    trap_script = f"""#!/bin/bash
echo "scp invoked with: $@" > "{trap}"
exit 42
"""
    with open(scp_path, "w") as f:
        f.write(trap_script)
    os.chmod(scp_path, 0o755)

    try:
        workspace.write_devmyaml()
        r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                           capture_output=True, timeout=180)
        assert r.returncode == 0, r.stderr.decode()

        # A brief write to force sync activity.
        (workspace.path / "trigger.txt").write_text("sync-me")
        subprocess.run([devm.path, "shell", "--", "true"],
                       cwd=str(workspace.path), timeout=30)

        assert not trap.exists(), \
            f"scp should never be invoked; log content: {trap.read_text()}"
    finally:
        # Restore.
        with open(scp_path, "wb") as f:
            f.write(backup.read_bytes())
        os.chmod(scp_path, 0o755)
