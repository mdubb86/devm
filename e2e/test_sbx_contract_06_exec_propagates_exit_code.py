"""exec: sbx exec NAME cmd runs cmd inside, propagates its exit code.

Verify both directions: a command that exits 0 returns 0; a command
that exits 17 returns 17.

Devm dependency: internal/orchestrator/ports.go waitForExecReady
polls `sbx exec NAME true` and trusts its returncode. Many devm
code paths (snapshot writes, readiness checks, in-VM probes) shell
out via Runner.Output / Runner.Run and check the exit code.
"""
from __future__ import annotations

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(90)
def test_exec_propagates_exit_code(sandbox_name):
    with contract_sandbox(minimal_kit(), sandbox_name):
        ok = sbx_exec(sandbox_name, "true")
        assert ok.returncode == 0, f"`true` should return 0; got {ok.returncode}"

        fail = sbx_exec(sandbox_name, "sh", "-c", "exit 17")
        assert fail.returncode == 17, (
            f"`exit 17` should be propagated; got {fail.returncode}"
        )
