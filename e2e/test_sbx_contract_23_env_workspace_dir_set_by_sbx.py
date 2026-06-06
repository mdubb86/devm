"""env: $WORKSPACE_DIR is set by sbx in all 5 consumers, including direct exe.

The sbx daemon sets WORKSPACE_DIR to the positional workspace path
and propagates it to:
  1. install:
  2. startup:
  3. interactive login bash
  4. non-interactive bash -c
  5. direct exe (no shell at all)  <- the surprise

Consumer 5 is the SURPRISE row: internal/orchestrator/apply_live.go:
105-106 today claims "non-interactive sbx exec does not set
WORKSPACE_DIR automatically (only available in the sbx daemon startup
context)" and uses an `-e WORKSPACE_DIR=<repoRoot>` workaround. The
empirical probe under sbx 0.31 shows WORKSPACE_DIR IS propagated
everywhere. If this test stays green, the workaround in apply_live.go
is obsolete and can be removed. If it fires, restore the workaround.

Devm dependency: every devm script under $WORKSPACE_DIR/.devm/* --
install-templates.sh, init-volumes.sh, devm-exec.sh -- references
WORKSPACE_DIR to find its own files. If unset, those scripts silently
break.
"""
from __future__ import annotations

import shutil
import tempfile

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(180)
def test_workspace_dir_set_in_all_consumers(sandbox_name):
    ws = tempfile.mkdtemp(prefix="contract-ENV-E-ws-")
    spec = minimal_kit(
        install=['printf "%s" "$WORKSPACE_DIR" > /tmp/install-ws'],
        startup=[{
            "command": ["sh", "-c", 'printf "%s" "$WORKSPACE_DIR" > /tmp/startup-ws'],
            "user": "1000",
            "description": "WORKSPACE_DIR startup probe",
        }],
    )

    try:
        with contract_sandbox(spec, sandbox_name, workspace=ws):
            # Consumer 1: install:
            r = sbx_exec(sandbox_name, "cat", "/tmp/install-ws")
            assert r.returncode == 0, f"install ws missing: {r.stderr.decode()}"
            assert r.stdout.decode() == ws, (
                f"WORKSPACE_DIR in install: was {r.stdout.decode()!r}, expected {ws!r}"
            )

            # Consumer 2: startup:
            r = sbx_exec(sandbox_name, "cat", "/tmp/startup-ws")
            assert r.returncode == 0, f"startup ws missing: {r.stderr.decode()}"
            assert r.stdout.decode() == ws, (
                f"WORKSPACE_DIR in startup: was {r.stdout.decode()!r}, expected {ws!r}"
            )

            # Consumer 3: interactive login bash.
            r = sbx_exec(sandbox_name, "bash", "-l", "-c", 'printf %s "$WORKSPACE_DIR"')
            assert r.returncode == 0, f"login bash failed: {r.stderr.decode()}"
            assert r.stdout.decode() == ws, (
                f"WORKSPACE_DIR in login bash was {r.stdout.decode()!r}"
            )

            # Consumer 4: non-interactive bash -c.
            r = sbx_exec(sandbox_name, "bash", "-c", 'printf %s "$WORKSPACE_DIR"')
            assert r.returncode == 0, f"bash -c failed: {r.stderr.decode()}"
            assert r.stdout.decode() == ws, (
                f"WORKSPACE_DIR in bash -c was {r.stdout.decode()!r}"
            )

            # Consumer 5: direct exe (no shell). THE SURPRISE ROW --
            # apply_live.go:105-106 claims this doesn't work; sbx 0.31
            # says it does.
            r = sbx_exec(sandbox_name, "printenv", "WORKSPACE_DIR")
            assert r.returncode == 0, (
                f"printenv WORKSPACE_DIR failed; sbx 0.31 may have regressed "
                f"to the pre-0.31 behavior where non-interactive exec "
                f"doesn't get WORKSPACE_DIR. Restore the -e WORKSPACE_DIR= "
                f"workaround in internal/orchestrator/apply_live.go.\n"
                f"stdout={r.stdout.decode()!r} stderr={r.stderr.decode()!r}"
            )
            assert r.stdout.decode().rstrip("\n") == ws, (
                f"WORKSPACE_DIR in direct exe was {r.stdout.decode()!r}"
            )
    finally:
        shutil.rmtree(ws, ignore_errors=True)
