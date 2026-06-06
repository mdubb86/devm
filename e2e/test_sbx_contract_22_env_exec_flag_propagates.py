"""env: sbx exec -e KEY=VALUE propagates the var into the called command.

For all three exec forms:
  3. interactive login bash (`-it bash -l -c`)
  4. non-interactive bash (`bash -c`)
  5. direct exe (`printenv`)

The -e flag must set FROM_EXEC_FLAG=exec-value in the called process's
environment. install: and startup: are N/A — those aren't dispatched
via sbx exec.

Devm dependency: sandbox.EnvArgs(cfg) renders forwarded host vars
(TERM, COLORTERM, LANG, LC_ALL, LC_CTYPE) as -e flags on every
interactive `sbx exec` call. Lost terminal capabilities would break
TUI rendering for the user's interactive shell.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.contract import contract_sandbox, minimal_kit

pytestmark = pytest.mark.sbx_contract

VAR = "FROM_EXEC_FLAG"
VAL = "exec-value"


def _exec_e(sandbox: str, *cmd: str) -> subprocess.CompletedProcess:
    """sbx exec -e VAR=VAL <sandbox> <cmd...>"""
    return subprocess.run(
        ["sbx", "exec", "-e", f"{VAR}={VAL}", sandbox, *cmd],
        capture_output=True, timeout=20,
    )


@pytest.mark.timeout(120)
def test_exec_dash_e_propagates_to_all_exec_forms(sandbox_name):
    with contract_sandbox(minimal_kit(), sandbox_name):
        # Consumer 3: interactive login bash.
        r = _exec_e(sandbox_name, "bash", "-l", "-c", f'printf %s "${VAR}"')
        assert r.returncode == 0, f"login bash failed: {r.stderr.decode()}"
        assert r.stdout.decode() == VAL, (
            f"-e in login bash was {r.stdout.decode()!r}, expected {VAL!r}"
        )

        # Consumer 4: non-interactive bash -c.
        r = _exec_e(sandbox_name, "bash", "-c", f'printf %s "${VAR}"')
        assert r.returncode == 0, f"bash -c failed: {r.stderr.decode()}"
        assert r.stdout.decode() == VAL, (
            f"-e in bash -c was {r.stdout.decode()!r}, expected {VAL!r}"
        )

        # Consumer 5: direct exe (no shell).
        r = _exec_e(sandbox_name, "printenv", VAR)
        assert r.returncode == 0, (
            f"printenv {VAR} failed; -e did NOT propagate to direct exe: "
            f"stdout={r.stdout.decode()!r} stderr={r.stderr.decode()!r}"
        )
        assert r.stdout.decode().rstrip("\n") == VAL, (
            f"-e in direct exe was {r.stdout.decode()!r}"
        )
