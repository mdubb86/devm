"""A cwd with no registered project resolves with a clear, actionable
error -- /vm/resolve-project's 404 body names `devm init` (see
internal/serviceapi/resolve_project.go), and every gated command
(`devm start` here) surfaces that body verbatim.

No devm.yaml, no VM: `devm start` fails at project resolution before
it ever reads a config file.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(20)
def test_unknown_cwd_prints_hint(devm_path, tmp_path):
    result = subprocess.run(
        [devm_path, "start"],
        cwd=str(tmp_path), capture_output=True, text=True, timeout=15,
    )
    assert result.returncode != 0
    assert "devm init" in result.stderr, result.stderr
