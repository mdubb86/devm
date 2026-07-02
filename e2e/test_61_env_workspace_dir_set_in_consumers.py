"""61: $WORKSPACE is set in all exec contexts (install, startup, direct exec).

WORKSPACE is devm's canonical name for the workspace path (see
internal/schema/env.go — the single reserved key alongside IS_SANDBOX).
It must be visible in:
  1. install: commands (run by the provisioner at cold-start)
  2. startup: (systemd service exec — via .devm/.env sourcing)
  3. direct tart exec (via with-devm-env wrapper)

Mechanism: devm renders WORKSPACE into .devm/.env; the with-devm-env
wrapper sources this file for direct exec. The provisioner routes
install: commands and systemd service execs through the same wrapper.

What this pins:
  - WORKSPACE is set in install: commands.
  - WORKSPACE is set in systemd service exec.
  - WORKSPACE is set in direct tart exec (via wrapper).

What it doesn't cover (tested elsewhere):
  - Workspace contents visible at WORKSPACE -> test_56.
  - General env: propagation -> test_60.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_workspace_set_in_all_consumers(workspace, devm, sandbox_name):
    ws = str(workspace.path)

    workspace.write_devmyaml(
        install=[
            'printf "%s" "$WORKSPACE" > /tmp/install-ws-61',
        ],
        services={
            "wsdir": {
                "exec": ["sh", "-c", 'printf "%s" "$WORKSPACE" > /tmp/startup-ws-61'],
                "restart": "no",
            },
        },
    )

    # Owns cold-start: install: commands only run at first `devm shell`, so
    # the test itself triggers cold-start after devm.yaml is in place.
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    tart_sandbox = TartSandbox(name=sandbox_name)
    assert tart_sandbox.state() == "running", (
        f"expected VM running; got {tart_sandbox.state()!r}"
    )

    # Consumer 1: install:
    r = tart_sandbox.exec_shell("cat /tmp/install-ws-61")
    assert r.ok, f"install-ws-61 missing: {r.stderr}"
    assert r.stdout == ws, (
        f"WORKSPACE in install: was {r.stdout!r}, expected {ws!r}"
    )

    # Consumer 2: startup (systemd service).
    r = tart_sandbox.exec_shell("cat /tmp/startup-ws-61")
    assert r.ok, f"startup-ws-61 missing: {r.stderr}"
    assert r.stdout == ws, (
        f"WORKSPACE in startup: was {r.stdout!r}, expected {ws!r}"
    )

    # Consumer 3: direct exec via with-devm-env wrapper.
    r = tart_sandbox.exec_shell(
        'with-devm-env sh -c \'printf "%s" "$WORKSPACE"\''
    )
    assert r.ok, f"wrapper exec failed: {r.stderr}"
    assert r.stdout == ws, (
        f"WORKSPACE via with-devm-env was {r.stdout!r}"
    )
