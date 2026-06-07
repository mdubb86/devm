"""install: bash is preinstalled on docker/sandbox-templates:shell.

Pins that bash is available before any install: step runs, so
devm-rendered shell wrappers can use bash-isms (pipefail, process
substitution `> >(...)`, `set -euo pipefail`) without an explicit
apt-install bash dependency.

Devm dependency: docs/superpowers/specs/2026-06-07-startup-
supervision-design.md uses bash for the wrap-step.sh wrapper (per-step
stdout/stderr split via `> >(s6-log ...)`). If bash were missing,
either the wrapper would need to bootstrap bash itself (chicken-and-
egg with install:) or fall back to POSIX sh + fifos.

Probe shape: install: itself is `command -v bash`. If bash is missing
from PATH, install: exits non-zero, sbx run fails per contract_02, and
contract_sandbox times out at the running-state poll. The fact that
the sandbox reaches running + exec-ready (which contract_sandbox waits
for) is itself proof that the install: step found bash.
"""
from __future__ import annotations

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(120)
def test_bash_present_before_install_phase(sandbox_name):
    # install: itself probes bash. If bash is missing, this command
    # fails, sbx run exits non-zero (contract_02), and contract_sandbox
    # raises during _wait_running.
    spec = minimal_kit(install=["command -v bash"])
    with contract_sandbox(spec, sandbox_name):
        # Belt: confirm bash exists post-bringup too. `command -v` is a
        # shell builtin — wrap in `sh -c` so sbx exec actually runs it.
        r = sbx_exec(sandbox_name, "sh", "-c", "command -v bash")
        assert r.returncode == 0, (
            f"bash missing on docker/sandbox-templates:shell after bringup: "
            f"stdout={r.stdout.decode()!r} stderr={r.stderr.decode()!r}"
        )
        path = r.stdout.decode().strip()
        assert path, f"empty path from `command -v bash`: {r.stdout!r}"

        # Suspenders: confirm bash actually executes (not just present).
        r = sbx_exec(sandbox_name, "bash", "-c", 'echo "rc=$?"')
        assert r.returncode == 0 and "rc=0" in r.stdout.decode(), (
            f"bash present but won't run trivial command: "
            f"stdout={r.stdout.decode()!r} stderr={r.stderr.decode()!r}"
        )
