"""env: kit env (environment.variables) reaches all 5 consumers.

A var declared in spec.yaml's environment.variables must be visible in:
  1. install: commands (run via sh by the sbx daemon at sandbox create)
  2. startup: commands (run via sh by the sbx daemon at every start)
  3. interactive login shell (`sbx exec -it NAME bash -l -c '...'`)
  4. non-interactive bash (`sbx exec NAME bash -c '...'`)
  5. direct exe (`sbx exec NAME printenv` — no shell at all)

One sandbox bringup; multiple assertions cover the consumers.

Devm dependency: devm renders cfg.Env into the kit's
environment.variables and relies on those vars being inherited by
every process the sbx daemon spawns inside the sandbox. IS_SANDBOX=1
(used by user scripts to detect "I'm running inside the sandbox") is
the canonical example.
"""
from __future__ import annotations

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract

EXPECTED = "kit-value"


@pytest.mark.timeout(180)
def test_kit_env_reaches_all_consumers(sandbox_name):
    spec = minimal_kit(
        extra_env={"FROM_KIT_TEST": EXPECTED},
        # install: writes FROM_KIT_TEST to /tmp/install-mark for consumer 1.
        install=['printf "%s" "$FROM_KIT_TEST" > /tmp/install-mark'],
        # startup: writes to /tmp/startup-mark for consumer 2.
        startup=[{
            "command": ["sh", "-c", 'printf "%s" "$FROM_KIT_TEST" > /tmp/startup-mark'],
            "user": "1000",
            "description": "startup env probe",
        }],
    )

    with contract_sandbox(spec, sandbox_name):
        # Consumer 1: install:
        r = sbx_exec(sandbox_name, "cat", "/tmp/install-mark")
        assert r.returncode == 0, f"install mark missing: {r.stderr.decode()}"
        assert r.stdout.decode() == EXPECTED, (
            f"FROM_KIT_TEST in install: was {r.stdout.decode()!r}, expected {EXPECTED!r}"
        )

        # Consumer 2: startup:
        r = sbx_exec(sandbox_name, "cat", "/tmp/startup-mark")
        assert r.returncode == 0, f"startup mark missing: {r.stderr.decode()}"
        assert r.stdout.decode() == EXPECTED, (
            f"FROM_KIT_TEST in startup: was {r.stdout.decode()!r}, expected {EXPECTED!r}"
        )

        # Consumer 3: interactive login bash.
        r = sbx_exec(sandbox_name, "bash", "-l", "-c", "printf %s \"$FROM_KIT_TEST\"")
        assert r.returncode == 0, f"login bash failed: {r.stderr.decode()}"
        assert r.stdout.decode() == EXPECTED, (
            f"FROM_KIT_TEST in login bash was {r.stdout.decode()!r}"
        )

        # Consumer 4: non-interactive bash -c.
        r = sbx_exec(sandbox_name, "bash", "-c", "printf %s \"$FROM_KIT_TEST\"")
        assert r.returncode == 0, f"bash -c failed: {r.stderr.decode()}"
        assert r.stdout.decode() == EXPECTED, (
            f"FROM_KIT_TEST in bash -c was {r.stdout.decode()!r}"
        )

        # Consumer 5: direct exe (no shell).
        r = sbx_exec(sandbox_name, "printenv", "FROM_KIT_TEST")
        assert r.returncode == 0, (
            f"printenv FROM_KIT_TEST failed; kit env NOT visible to direct exe: "
            f"stdout={r.stdout.decode()!r} stderr={r.stderr.decode()!r}"
        )
        assert r.stdout.decode().rstrip("\n") == EXPECTED, (
            f"FROM_KIT_TEST in direct exe was {r.stdout.decode()!r}"
        )
