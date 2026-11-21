"""53: devm teardown --yes removes the VM entirely.

After cold-start, `devm teardown --yes` must make the sandbox disappear
from `tart list` (state == "absent"). No stopped VM remnant is left.

What this pins:
  - devm teardown --yes transitions running -> absent.
  - tart list no longer shows the VM after teardown.

What it doesn't cover (tested elsewhere):
  - Stop-only (not removal) -> test_52.
  - Interactive teardown prompt -> test_05.
"""
from __future__ import annotations

import time

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(120)
def test_teardown_destroys_vm(devm, tart_sandbox):
    # tart_sandbox fixture already cold-started the VM.
    before = tart_sandbox.state()
    assert before != "absent", (
        f"VM should exist before teardown; got {before!r}"
    )

    devm.teardown(yes=True, timeout=30)

    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if tart_sandbox.state() == "absent":
            return
        time.sleep(0.5)
    pytest.fail(
        f"VM still present after teardown; state={tart_sandbox.state()!r}"
    )
