"""Lifecycle helpers — shared shapes that drive devm CLI state transitions.

Extracted from per-test inline boilerplate. If a pattern is used by
three or more test files, it belongs here.
"""
from __future__ import annotations

import time


def stop_and_wait_stopped(devm, sandbox_name: str, *, timeout: float = 15.0) -> None:
    """Run `devm stop --yes` and poll until the sandbox reaches 'stopped'.

    Used at the tail of nearly every devm e2e test. After the sbx → Tart
    migration the poll will use `tart list`; the stub below issues the
    stop command and waits a fixed interval so surviving tests can collect
    and run while Tasks 17/18 rewrite the callers.
    """
    devm.stop(yes=True)
    time.sleep(2.0)
