"""`devm init` refuses to re-register a cwd that's already bound to a
different project name -- /vm/register-project's FindProjectByCwd
check (internal/serviceapi/register_project.go) rejects with 409
before touching the second name's state dir.
"""
from __future__ import annotations

import shutil
import subprocess
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(30)
def test_init_rejects_registered_cwd(devm_path, sandbox_name, tmp_path):
    name_a = sandbox_name
    name_b = f"{sandbox_name}-second"
    state_dir = Path.home() / "Library" / "Application Support" / "devm-e2e" / name_a
    try:
        first = subprocess.run(
            [devm_path, "init", name_a],
            cwd=str(tmp_path), capture_output=True, text=True, timeout=15,
        )
        assert first.returncode == 0, first.stderr

        second = subprocess.run(
            [devm_path, "init", name_b],
            cwd=str(tmp_path), capture_output=True, text=True, timeout=15,
        )
        assert second.returncode != 0, "init on an already-registered cwd should fail"
        assert "already registered" in second.stderr, second.stderr
    finally:
        shutil.rmtree(state_dir, ignore_errors=True)
