"""lifecycle: `sbx run --kit X --name Y AGENT WS` brings up a sandbox.

Canonical baseline. A minimal kit (no-op install, no-op startup,
sleep-infinity entrypoint) must reach the 'running' state and be
exec-ready. Every other contract test implicitly relies on this; if
L1 breaks, almost everything in the cohort will fail too.

Devm dependency: internal/orchestrator/shell.go cold-start spawns
`nohup sbx run …`, polls `sb.IsRunning()` until true, then calls
waitForExecReady. If this contract breaks, devm cold-start can never
succeed.
"""
from __future__ import annotations

import pytest

from helpers import sbx
from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(120)
def test_sbx_run_brings_up_minimal_sandbox(sandbox_name):
    with contract_sandbox(minimal_kit(), sandbox_name):
        assert sbx.sandbox_state(sandbox_name) == "running", (
            f"sandbox should be 'running' once contract_sandbox yields; "
            f"got {sbx.sandbox_state(sandbox_name)!r}"
        )
        # And a no-op exec should succeed (proves exec-ready genuinely).
        assert sbx_exec(sandbox_name, "true").returncode == 0
