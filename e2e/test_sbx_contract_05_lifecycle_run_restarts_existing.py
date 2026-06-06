"""lifecycle: sbx run --kit X NAME restarts existing stopped sandbox.

After creating a sandbox (sbx run --kit X --name Y AGENT WS) and
stopping it, you can bring it back up by calling `sbx run --kit X NAME`
with NO --name, NO agent, NO workspace positionals.

Devm dependency: internal/orchestrator/shell.go cold-start branches
on sb.Exists(). When the sandbox already exists but isn't running,
devm calls this restart form. If sbx changed the restart CLI shape,
devm's restart path would break silently — cold-create would seem to
work but recreate the sandbox from scratch.
"""
from __future__ import annotations

import os
import shutil
import subprocess
import tempfile
import time

import pytest

from helpers import sbx
from helpers.contract import minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(180)
def test_run_with_kit_and_name_only_restarts_existing(sandbox_name):
    spec = minimal_kit()
    # Manage kit dir ourselves so it survives the stop/restart cycle
    # (contract_sandbox would tear down its own kit dir).
    kit_dir = tempfile.mkdtemp(prefix="contract-L6-kit-")
    ws = tempfile.mkdtemp(prefix="contract-L6-ws-")
    try:
        with open(os.path.join(kit_dir, "spec.yaml"), "w") as f:
            f.write(spec)

        # Initial create (the full form).
        create = subprocess.Popen(
            ["sbx", "run", "--kit", kit_dir, "--name", sandbox_name,
             "probe", ws],
            stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
        )
        try:
            deadline = time.monotonic() + 90
            while time.monotonic() < deadline:
                if sbx.sandbox_state(sandbox_name) == "running":
                    break
                time.sleep(0.5)
            assert sbx.sandbox_state(sandbox_name) == "running"

            # Plant a marker so we can verify restart != recreate.
            r = sbx_exec(sandbox_name, "sh", "-c", "touch /home/agent/restart-marker")
            assert r.returncode == 0
        finally:
            try:
                create.kill()
                create.wait(timeout=5)
            except Exception:
                pass

        # Stop the sandbox.
        subprocess.run(["sbx", "stop", sandbox_name], capture_output=True, timeout=30)
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if sbx.sandbox_state(sandbox_name) == "stopped":
                break
            time.sleep(0.5)
        assert sbx.sandbox_state(sandbox_name) == "stopped"

        # Restart with the L6 form: --kit X and NAME only.
        restart = subprocess.Popen(
            ["sbx", "run", "--kit", kit_dir, sandbox_name],
            stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
        )
        try:
            deadline = time.monotonic() + 60
            while time.monotonic() < deadline:
                if sbx.sandbox_state(sandbox_name) == "running":
                    break
                time.sleep(0.5)
            assert sbx.sandbox_state(sandbox_name) == "running", (
                f"restart form failed to bring up sandbox; "
                f"state={sbx.sandbox_state(sandbox_name)!r}"
            )
            # Marker survived → it was a restart, not a recreate.
            check = sbx_exec(sandbox_name, "test", "-f", "/home/agent/restart-marker")
            assert check.returncode == 0, (
                "marker file missing after restart — sbx may have recreated "
                "the sandbox from scratch instead of restarting it"
            )
        finally:
            try:
                restart.kill()
                restart.wait(timeout=5)
            except Exception:
                pass
    finally:
        subprocess.run(["sbx", "stop", sandbox_name], capture_output=True, timeout=15)
        subprocess.run(["sbx", "rm", "-f", sandbox_name], capture_output=True, timeout=15)
        shutil.rmtree(kit_dir, ignore_errors=True)
        shutil.rmtree(ws, ignore_errors=True)
