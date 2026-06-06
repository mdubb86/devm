"""install/startup: $WORKSPACE_DIR is set + workspace mount visible.

In both `install:` and `startup:` contexts, the env var WORKSPACE_DIR
must be set to the workspace path AND the workspace contents must be
visible at that path. Parametrized across the two phases.

The probe writes a marker file dumping WORKSPACE_DIR + ls of it. We
read the marker post-bringup and assert the env var matches the
workspace path AND a sentinel file is visible.

Devm dependency: every devm-managed script
(internal/scripts/init-volumes.sh, install-templates.sh, bootstrap.sh)
references files under $WORKSPACE_DIR/.devm/. If WORKSPACE_DIR is
unset in either phase, those scripts silently degrade.
"""
from __future__ import annotations

import os
import shutil
import tempfile

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract

SENTINEL = "CONTRACT_I1_WORKSPACE_SENTINEL"

# Single shell snippet dumps WORKSPACE_DIR + ls of it to the marker.
PROBE_SH = (
    'echo "WORKSPACE_DIR=$WORKSPACE_DIR" > {out}; '
    'ls -la "$WORKSPACE_DIR" >> {out} 2>&1'
)


@pytest.mark.timeout(180)
@pytest.mark.parametrize("phase", ["install", "startup"], ids=["install", "startup"])
def test_workspace_dir_set_in(sandbox_name, phase):
    ws = tempfile.mkdtemp(prefix="contract-I1-ws-")
    with open(os.path.join(ws, SENTINEL), "w") as f:
        f.write("present\n")
    out = f"/tmp/contract-I1-{phase}-out.txt"
    sh = PROBE_SH.format(out=out)

    if phase == "install":
        # install: entries are shell command strings.
        kit = minimal_kit(install=[sh])
    else:
        # startup: entries are dicts with a command list (argv).
        kit = minimal_kit(startup=[{
            "command": ["sh", "-c", sh],
            "user": "1000",
            "description": "I1 startup probe",
        }])

    try:
        with contract_sandbox(kit, sandbox_name, workspace=ws):
            r = sbx_exec(sandbox_name, "cat", out)
            assert r.returncode == 0, f"probe output missing: {r.stderr.decode()}"
            text = r.stdout.decode()
            assert f"WORKSPACE_DIR={ws}" in text, (
                f"WORKSPACE_DIR not set to workspace in {phase}: {text!r}"
            )
            assert SENTINEL in text, (
                f"workspace sentinel not visible from {phase}: {text!r}"
            )
    finally:
        shutil.rmtree(ws, ignore_errors=True)
