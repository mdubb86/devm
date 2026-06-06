"""lifecycle: sbx stop NAME transitions to stopped, preserves state.

After `sbx run` brings up a sandbox, writing a file inside, then
`sbx stop NAME` must:
  1. Move the sandbox to 'stopped' state in `sbx ls`
  2. Preserve the file contents (it must still be there after restart)

`devm stop` calls `sbx stop` (without -f). If sbx stop wiped state,
the anchor-alive contract would be meaningless and `devm shell`
after `devm stop` would always look like a cold create.

Devm dependency: internal/orchestrator/stop.go runs `sbx stop NAME`
when mode != StopDestroy.
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers import sbx
from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(180)
def test_stop_preserves_filesystem_state(sandbox_name):
    spec = minimal_kit()
    with contract_sandbox(spec, sandbox_name):
        # Write a marker file inside the sandbox.
        r = sbx_exec(sandbox_name, "sh", "-c", "echo hello > /home/agent/marker.txt")
        assert r.returncode == 0, f"failed to write marker: {r.stderr.decode()}"

        # Stop the sandbox.
        stop = subprocess.run(["sbx", "stop", sandbox_name], capture_output=True, timeout=30)
        assert stop.returncode == 0, f"sbx stop failed: {stop.stderr.decode()}"

        # Poll briefly for state transition.
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if sbx.sandbox_state(sandbox_name) == "stopped":
                break
            time.sleep(0.5)
        assert sbx.sandbox_state(sandbox_name) == "stopped", (
            f"sandbox should be 'stopped' after sbx stop; "
            f"got {sbx.sandbox_state(sandbox_name)!r}"
        )

        # Restart with the L5 form (sbx run NAME) and verify the file survived.
        proc = subprocess.Popen(
            ["sbx", "run", sandbox_name],
            stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
        )
        try:
            deadline = time.monotonic() + 60
            while time.monotonic() < deadline:
                if sbx.sandbox_state(sandbox_name) == "running":
                    break
                time.sleep(0.5)
            assert sbx.sandbox_state(sandbox_name) == "running"
            check = sbx_exec(sandbox_name, "cat", "/home/agent/marker.txt")
            assert check.returncode == 0, f"marker missing after restart: {check.stderr.decode()}"
            assert check.stdout.decode().strip() == "hello", (
                f"marker corrupted after restart: {check.stdout.decode()!r}"
            )
        finally:
            try:
                proc.kill()
                proc.wait(timeout=5)
            except Exception:
                pass
