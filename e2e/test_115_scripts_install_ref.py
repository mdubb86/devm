"""115: `scripts:` — named multi-command snippets, referenced from
`install:` via a `>NAME` entry.

Pins the end-to-end path landed across the named-scripts feature
(schema.Config.Scripts -> render.ProvisionScriptInput.Scripts ->
Provisioner.scriptInput() -> RenderProvisionScript): a `>write-marker`
entry in `install:` expands to the named script's commands, joined
with ` && ` and run under one `bash -eo pipefail -c` invocation, so a
variable set in one step of the script is visible in a later step of
the SAME script.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm

MARKER = "/home/devm/.devm-scripts-marker"


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_scripts_install_ref_expands_and_shares_shell(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        scripts={
            "write-marker": [
                f'MARKER="{MARKER}"',
                'echo hello > "$MARKER"',
            ],
        },
        install=[">write-marker"],
    )

    shell = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=480,
    )
    assert shell.returncode == 0, f"cold start failed: {shell.stderr.decode()!r}"

    r = devm_exec_with_retry(
        devm.path, ["cat", MARKER],
        cwd=str(workspace.path), timeout=30,
    )
    assert r.returncode == 0, r.stderr.decode()
    assert r.stdout.decode().strip() == "hello"
