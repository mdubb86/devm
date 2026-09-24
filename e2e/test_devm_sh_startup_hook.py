"""devm.sh startup() magic function fires at each boot, gets the same env
startup: entries get, and its non-zero exit fails cold-start.
"""
from __future__ import annotations
import subprocess
import time
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
def test_startup_hook_runs_every_boot(workspace, devm):
    workspace.write_devmyaml(env={"HELLO": "world"})
    workspace.write_devm_sh(
        "#!/usr/bin/env bash\n"
        "set -eo pipefail\n"
        "startup() {\n"
        '  echo "HELLO=${HELLO:-<unset>} TS=$(date +%s)" > /home/devm/startup-marker\n'
        "}\n"
    )
    try:
        first_start = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert first_start.returncode == 0, first_start.stderr.decode()

        read_first = subprocess.run(
            [devm.path, "shell", "--", "cat", "/home/devm/startup-marker"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert read_first.returncode == 0
        first_content = read_first.stdout.decode()
        assert "HELLO=world" in first_content

        time.sleep(2)

        stop = subprocess.run(
            [devm.path, "stop", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=120,
        )
        assert stop.returncode == 0, stop.stderr.decode()

        second_start = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert second_start.returncode == 0, second_start.stderr.decode()

        read_second = subprocess.run(
            [devm.path, "shell", "--", "cat", "/home/devm/startup-marker"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert read_second.returncode == 0
        second_content = read_second.stdout.decode()
        assert "HELLO=world" in second_content

        assert first_content != second_content, (
            "startup marker must be rewritten on every boot; "
            f"first={first_content!r}, second={second_content!r}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
