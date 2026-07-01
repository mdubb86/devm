"""61: $WORKSPACE_DIR is set in all exec contexts (install, startup, exec).

WORKSPACE_DIR is set by the VM environment and must be visible in:
  1. install: commands (run by the provisioner at cold-start)
  2. startup: (systemd service exec — via .devm/.env sourcing)
  3. direct tart exec (via with-devm-env wrapper)

Ship 4 mechanism: devm renders WORKSPACE_DIR into .devm/.env alongside
WORKSPACE (both point to the repo root / workspace VM path). The
with-devm-env wrapper sources this file. The provisioner runs install:
commands with the workspace path already in the environment.

Every devm script under $WORKSPACE_DIR/.devm/* (install-templates.sh,
devm-startup.sh, wrap-fg.sh) depends on WORKSPACE_DIR to find its own
files. If unset in any context, those scripts silently break.

What this pins:
  - WORKSPACE_DIR is set in install: commands.
  - WORKSPACE_DIR is set in systemd service exec.
  - WORKSPACE_DIR is set in direct tart exec (via wrapper).

What it doesn't cover (tested elsewhere):
  - Workspace contents visible at WORKSPACE_DIR -> test_56.
  - General env: propagation -> test_60.
"""
from __future__ import annotations

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.xfail(
    strict=False,
    reason=(
        "devm bug L (new): provisioner's runInstallCommands runs `tart exec bash -c` "
        "without sourcing .devm/.env, so WORKSPACE_DIR is not set during install: "
        "commands — install-ws-61 is absent. Additionally: devm bug K "
        "(systemdQuoteArgv) causes the startup service to mis-exec its sh -c command, "
        "leaving no startup-ws-61. devm bug F (workspace not mounted) means the "
        "with-devm-env exec also cannot find .devm/.env. Also note: this test writes "
        "devmyaml AFTER the tart_sandbox fixture has already cold-started (test "
        "ordering bug). Remove xfail when bugs F, K, and L land."
    ),
)
@pytest.mark.timeout(180)
def test_workspace_dir_set_in_all_consumers(workspace, devm, tart_sandbox):
    ws = str(workspace.path)

    workspace.write_devmyaml(
        install=[
            'printf "%s" "$WORKSPACE_DIR" > /tmp/install-ws-61',
        ],
        services={
            "wsdir": {
                "exec": ["sh", "-c", 'printf "%s" "$WORKSPACE_DIR" > /tmp/startup-ws-61'],
                "restart": "no",
            },
        },
    )

    assert tart_sandbox.state() == "running", (
        f"expected VM running; got {tart_sandbox.state()!r}"
    )

    # Consumer 1: install:
    r = tart_sandbox.exec_shell("cat /tmp/install-ws-61")
    assert r.ok, f"install-ws-61 missing: {r.stderr}"
    assert r.stdout == ws, (
        f"WORKSPACE_DIR in install: was {r.stdout!r}, expected {ws!r}"
    )

    # Consumer 2: startup (systemd service).
    r = tart_sandbox.exec_shell("cat /tmp/startup-ws-61")
    assert r.ok, f"startup-ws-61 missing: {r.stderr}"
    assert r.stdout == ws, (
        f"WORKSPACE_DIR in startup: was {r.stdout!r}, expected {ws!r}"
    )

    # Consumer 3: direct exec via with-devm-env wrapper.
    r = tart_sandbox.exec_shell(
        'with-devm-env sh -c \'printf "%s" "$WORKSPACE_DIR"\''
    )
    assert r.ok, f"wrapper exec failed: {r.stderr}"
    assert r.stdout == ws, (
        f"WORKSPACE_DIR via with-devm-env was {r.stdout!r}"
    )
