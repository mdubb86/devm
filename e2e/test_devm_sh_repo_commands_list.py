"""repos.<id>.commands registers a devm.sh/devm.me.sh function as
runnable in-guest via `run <name>`; a function not listed there is
refused even though it exists in devm.sh.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_repo_commands_list_registers_and_denies(workspace, devm):
    workspace.write_devmyaml(
        repos={
            "main": {
                "url": workspace.bare_repo_url(),
                "primary": True,
                "commands": ["list-files"],
            },
        },
    )
    workspace.write_devm_sh(
        "#!/usr/bin/env bash\n"
        "set -eo pipefail\n"
        "list-files() {\n"
        "  ls\n"
        "}\n"
        "not-listed() {\n"
        "  echo unauthorized\n"
        "}\n"
    )
    try:
        cold = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=240,
        )
        assert cold.returncode == 0, f"start failed: {cold.stderr.decode()!r}"

        label = workspace.bare_repo_label()

        allowed = subprocess.run(
            [devm.path, "exec", "bash", "-c", f"cd /home/devm/{label} && run list-files"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert allowed.returncode == 0, (
            f"run list-files failed: {allowed.stderr.decode()!r}"
        )

        denied = subprocess.run(
            [devm.path, "exec", "bash", "-c", f"cd /home/devm/{label} && run not-listed"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert denied.returncode != 0, "run not-listed must be refused"
        assert "not registered" in denied.stderr.decode(), denied.stderr.decode()
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
