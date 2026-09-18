"""`devm init <name>` registers the current directory as a devm
project: it POSTs to the daemon's /vm/register-project, which seeds a
devm.yaml under the daemon's state dir and records a cwd -> project
alias.

Uses `devm_path` + `tmp_path` directly rather than the `workspace`
fixture -- `workspace` already runs `devm init` for its own
sandbox_name during setup, and this test wants a fresh, unregistered
cwd to init itself.
"""
from __future__ import annotations

import shutil
import subprocess
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(30)
def test_init_creates_project(devm_path, sandbox_name, tmp_path):
    config_path = (
        Path.home() / "Library" / "Application Support" / "devm-e2e"
        / sandbox_name / "devm.yaml"
    )
    try:
        result = subprocess.run(
            [devm_path, "init", sandbox_name],
            cwd=str(tmp_path), capture_output=True, text=True, timeout=15,
        )
        assert result.returncode == 0, result.stderr
        assert config_path.exists(), f"no devm.yaml written at {config_path}"
        assert f"name: {sandbox_name}" in config_path.read_text()
    finally:
        shutil.rmtree(config_path.parent, ignore_errors=True)
