"""55: tart exec propagates the inner command's exit code.

Verify both directions: a command that exits 0 returns 0; a command
that exits 17 returns 17.

What this pins:
  - tart_sandbox.exec("true") returns exit_code == 0.
  - tart_sandbox.exec_shell("exit 17") returns exit_code == 17.

What it doesn't cover (tested elsewhere):
  - Cold-start exec-ready baseline -> test_50.
"""
from __future__ import annotations

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(90)
def test_exec_propagates_exit_code(tart_sandbox):
    # tart_sandbox fixture already cold-started the VM.
    ok = tart_sandbox.exec("true")
    assert ok.exit_code == 0, f"`true` should return 0; got {ok.exit_code}"

    fail = tart_sandbox.exec_shell("exit 17")
    assert fail.exit_code == 17, (
        f"`exit 17` should be propagated; got {fail.exit_code}"
    )
