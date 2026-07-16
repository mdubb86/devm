"""88: user `install:` runs once on first boot, not on every restart.

Pins the first-boot marker (/var/lib/devm/provisioned): a devm stop +
devm shell restart must NOT re-run install: commands.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers import stop_and_wait_stopped
from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm

SENTINEL = "/home/devm/.devm-install-count"


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_install_runs_once_across_restart(workspace, devm, sandbox_name):
    workspace.write_devmyaml(install=[f"echo run >> {SENTINEL}"])

    shell = subprocess.run([devm.path, "shell", "--", "true"],
                           cwd=str(workspace.path), capture_output=True, timeout=480)
    assert shell.returncode == 0, f"cold start failed: {shell.stderr.decode()!r}"

    def count() -> int:
        r = devm_exec_with_retry(devm.path, ["sh", "-c", f"wc -l < {SENTINEL} 2>/dev/null || echo 0"],
                                 cwd=str(workspace.path), timeout=30)
        return int(r.stdout.decode().strip() or "0")

    assert count() == 1, "install: should have run exactly once on first boot"

    stop_and_wait_stopped(devm, sandbox_name)
    reshell = subprocess.run([devm.path, "shell", "--", "true"],
                             cwd=str(workspace.path), capture_output=True, timeout=300)
    assert reshell.returncode == 0, f"restart failed: {reshell.stderr.decode()!r}"

    assert count() == 1, "install: must NOT re-run on restart (marker gates it)"
