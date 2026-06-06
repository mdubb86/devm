"""lifecycle: sbx rm -f NAME removes the sandbox entirely.

After `sbx run` brings up a sandbox, `sbx rm -f NAME` must:
  1. Return non-interactively (sbx 0.29 added a confirmation prompt;
     -f bypasses it — without -f, sbx hangs waiting on stdin)
  2. Make the sandbox disappear from `sbx ls`

Devm dependency: internal/orchestrator/stop.go runs `sbx rm -f` for
StopDestroy mode. Without -f, `devm teardown` would hang (test_05/06
failure pattern after the 0.29 upgrade).
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers import sbx
from helpers.contract import contract_sandbox, minimal_kit

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(120)
def test_rm_force_destroys_sandbox(sandbox_name):
    spec = minimal_kit()
    with contract_sandbox(spec, sandbox_name):
        assert sbx.sandbox_exists(sandbox_name)

        # rm -f must succeed non-interactively and return promptly.
        r = subprocess.run(
            ["sbx", "rm", "-f", sandbox_name],
            capture_output=True, timeout=30,
        )
        assert r.returncode == 0, f"sbx rm -f failed: {r.stderr.decode()}"

        # Sandbox should be gone within a few seconds.
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if not sbx.sandbox_exists(sandbox_name):
                return
            time.sleep(0.5)
        assert not sbx.sandbox_exists(sandbox_name), (
            f"sandbox still listed after rm -f: {sbx.sandbox_state(sandbox_name)!r}"
        )
