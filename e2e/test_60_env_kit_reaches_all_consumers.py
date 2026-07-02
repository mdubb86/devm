"""60: env: key-value map reaches install:, startup: (service), and exec contexts.

A var declared in devm.yaml's top-level `env:` map must be visible in:
  1. install: commands (run by the provisioner at cold-start)
  2. startup: (systemd service exec)
  3. interactive exec via tart (direct exec; wrapper sources .devm/.env)

devm renders cfg.Env into .devm/.env as `export KEY='value'` lines. The
with-devm-env wrapper sources this file. Systemd services get the same env
via rendered Environment= lines; the interactive shell sources via wrapper.

What this pins:
  - env: vars are visible in install: commands.
  - env: vars are visible in systemd service exec.
  - env: vars are visible in direct tart exec (via wrapper).

What it doesn't cover (tested elsewhere):
  - $WORKSPACE expansion in env values -> test_26.
  - WORKSPACE availability across consumers -> test_61.
  - Service-level env (services.X.env) -> covered by systemd Environment= in
    service unit rendering tests.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm

EXPECTED = "kit-value-60"


@pytest.mark.timeout(180)
def test_kit_env_reaches_all_consumers(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        env={"FROM_KIT_60": EXPECTED},
        install=[
            'printf "%s" "$FROM_KIT_60" > /tmp/install-mark-60',
        ],
        services={
            "envcheck": {
                "exec": ["sh", "-c", 'printf "%s" "$FROM_KIT_60" > /tmp/startup-mark-60'],
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

    # Consumer 1: install: command.
    r = tart_sandbox.exec_shell("cat /tmp/install-mark-60")
    assert r.ok, f"install mark missing: {r.stderr}"
    assert r.stdout == EXPECTED, (
        f"FROM_KIT_60 in install: was {r.stdout!r}, expected {EXPECTED!r}"
    )

    # Consumer 2: systemd service exec.
    r = tart_sandbox.exec_shell("cat /tmp/startup-mark-60")
    assert r.ok, f"startup mark missing: {r.stderr}"
    assert r.stdout == EXPECTED, (
        f"FROM_KIT_60 in startup: was {r.stdout!r}, expected {EXPECTED!r}"
    )

    # Consumer 3: interactive exec via with-devm-env wrapper.
    r = tart_sandbox.exec_shell(
        'with-devm-env sh -c \'printf "%s" "$FROM_KIT_60"\''
    )
    assert r.ok, f"wrapper exec failed: {r.stderr}"
    assert r.stdout == EXPECTED, (
        f"FROM_KIT_60 via with-devm-env was {r.stdout!r}"
    )
