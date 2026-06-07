"""startup failure: a file written to $WORKSPACE_DIR by a failing startup:
step persists on the host (sandbox stays alive per c24).

Companion to c32. Startup failures are silent (c24) — sbx leaves the
sandbox running. So the workspace mount is still mounted, and the write
trivially persists from the host's POV. This is the easy case but worth
pinning so the wrapper's `also write to mount` codepath has explicit
contract coverage for startup too (not just install).

Devm dependency: same as c32/c33 — the supervision design refinement
relies on this property for startup failures.
"""
from __future__ import annotations

import os
import subprocess
import tempfile

import pytest

from helpers.contract import contract_sandbox, minimal_kit

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(120)
def test_startup_failure_workspace_write_persists_on_host(sandbox_name):
    ws = tempfile.mkdtemp(prefix="probe-c34-ws-")
    try:
        spec = minimal_kit(
            install=["true"],
            startup=[
                {
                    "command": ["sh", "-c",
                                'echo STARTUP_FAILED > "$WORKSPACE_DIR/probe.out"; exit 1'],
                    "user": "1000",
                    "description": "failing startup with mount-write",
                },
            ],
        )

        with contract_sandbox(spec, sandbox_name, workspace=ws):
            # The sandbox is up (silent failure per c24). Host-side
            # file must exist with the written content.
            host_path = os.path.join(ws, "probe.out")
            assert os.path.exists(host_path), (
                f"VM-side startup write to $WORKSPACE_DIR not visible on host"
            )
            with open(host_path) as f:
                assert f.read().rstrip() == "STARTUP_FAILED"

            # Document UID/perms for the record (startup default user is 1000).
            st = os.stat(host_path)
            print(f"observed UID={st.st_uid} GID={st.st_gid} mode={oct(st.st_mode & 0o777)}")
            print(f"host process EUID={os.geteuid()}")

            # And the host can remove it without sudo.
            os.remove(host_path)
            assert not os.path.exists(host_path)
    finally:
        import shutil
        shutil.rmtree(ws, ignore_errors=True)
