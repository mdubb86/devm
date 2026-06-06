"""Lifecycle helpers — shared shapes that drive devm CLI state transitions.

Extracted from per-test inline boilerplate. If a pattern is used by
three or more test files, it belongs here.
"""
from __future__ import annotations

import time

import pytest

from helpers import sbx


def stop_and_wait_stopped(devm, sandbox_name: str, *, timeout: float = 15.0) -> None:
    """Run `devm stop --yes` and poll until the sandbox reaches 'stopped'.

    Used at the tail of nearly every devm e2e test under the anchor-alive
    architecture: the user shell exit no longer auto-stops the sandbox,
    so the test must explicitly wind it down before the next test starts.
    """
    devm.stop(yes=True)
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "stopped":
            return
        time.sleep(0.5)
    pytest.fail(
        f"sandbox {sandbox_name} never reached 'stopped' within {timeout}s"
    )
