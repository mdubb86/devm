"""`devm init` is gone -- the verb is unregistered, so cobra rejects it
as an unknown command rather than devm running any init logic. Pins
the no-init-verb decision from
docs/superpowers/specs/2026-09-23-devm-yaml-back-in-repo-design.md.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(30)
def test_devm_init_is_unknown_command(devm_path):
    result = subprocess.run(
        [devm_path, "init"],
        capture_output=True, timeout=15,
    )
    assert result.returncode != 0, "devm init unexpectedly succeeded"

    combined = (result.stdout + result.stderr).decode().lower()
    assert "unknown command" in combined, (
        f"expected cobra's unknown-command error; got:\n"
        f"stdout={result.stdout.decode()!r}\nstderr={result.stderr.decode()!r}"
    )
