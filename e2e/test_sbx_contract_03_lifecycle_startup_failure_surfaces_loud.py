"""lifecycle: a failing startup: step logs a loud error in sbx output.

sbx 0.31 does NOT propagate startup-step failures into sbx run's
exit code or stop the sandbox — the sandbox still comes up to
'running' state and sbx run continues as the anchor. But the
failure IS logged on sbx's terminal output (which devm captures
via the PTY anchor + ring buffer).

This is a sharp diverge from L2 (install failure blocks creation
and surfaces via non-zero rc). For startup, sbx's 'loud' signal is
text in the captured output, not a process-level signal.

Devm dependency: internal/orchestrator/shell.go captures the PTY
output into the anchor ring buffer. The ring buffer is currently
only surfaced via the cleanup defer on cold-start failure paths —
so a successful cold-start with a silently-failed startup step
slips past devm today. This contract test pins the assumption that
the error IS in the captured stream, so a future devm-side scan
(after exec-ready, before user-shell handoff) can rely on it.
"""
from __future__ import annotations

import time

import pytest

from helpers import sbx
from helpers.contract import contract_sandbox, minimal_kit

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(120)
def test_startup_failure_logs_error_in_pty_output(sandbox_name):
    spec = minimal_kit(startup=[
        {
            "command": ["sh", "-c", "exit 1"],
            "user": "1000",
            "description": "L3 deliberately-failing startup",
        },
    ])
    with contract_sandbox(spec, sandbox_name) as ctx:
        # Sandbox came up despite the failing startup — this is sbx 0.31's
        # actual behavior, distinct from L2.
        assert sbx.sandbox_state(sandbox_name) == "running", (
            f"sandbox should still reach running despite startup failure; "
            f"got state={sbx.sandbox_state(sandbox_name)!r}"
        )

        # sbx logs the startup-step inspection error somewhat after
        # the sandbox reaches exec-ready (the failure is detected when
        # sbx tries to inspect the exec process result). 20s is enough
        # empirically.
        time.sleep(20)

        captured = ctx.captured()
        # The "loud" half: sbx must log an error in the captured output.
        # Devm's PTY anchor (Tier 1c) puts this into the ring buffer.
        assert "ERROR" in captured.upper(), (
            f"sbx should print an error line when a startup step fails. "
            f"Without it, devm cannot detect the failure (the sandbox is "
            f"otherwise indistinguishable from a healthy one).\n"
            f"\n--- captured PTY output ---\n{captured}\n--- end ---"
        )
