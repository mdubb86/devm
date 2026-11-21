"""devm.me.sh is sourced after devm.sh (cmd/run and the install-phase
sourcing both do `source devm.sh; source devm.me.sh`), so a function
devm.me.sh redefines wins over devm.sh's own definition of the same
name.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_devm_me_sh_override_wins_last_source(workspace, devm):
    workspace.write_devmyaml()
    workspace.write_devm_sh(
        "#!/usr/bin/env bash\n"
        "set -eo pipefail\n"
        "install() {\n"
        "  echo v1 > /home/devm/mark\n"
        "}\n"
    )
    workspace.write_devm_me_sh(
        "#!/usr/bin/env bash\n"
        "install() {\n"
        "  echo v2 > /home/devm/mark\n"
        "}\n"
    )
    try:
        cold = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=240,
        )
        assert cold.returncode == 0, f"start failed: {cold.stderr.decode()!r}"

        got = subprocess.run(
            [devm.path, "exec", "cat", "/home/devm/mark"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert got.returncode == 0, got.stderr.decode()
        assert "v2" in got.stdout.decode(), (
            f"expected devm.me.sh's install() to win, got: {got.stdout.decode()!r}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
