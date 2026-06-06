"""lifecycle: a failing install: step makes sbx exit non-zero, no zombie sandbox.

When any install: step exits non-zero, `sbx run` MUST exit non-zero
too AND leave no half-created sandbox behind. A silently-broken
sandbox would defeat devm's loud-failure UX
(internal/orchestrator/shell.go's anchor ring buffer captures sbx
run's output, but only as far as sbx itself reports the failure
honestly).

Devm dependency: the ring buffer + handedOff defer in shell.go only
help diagnostically if sbx propagates the install failure. If sbx
silently ignored a failing install, the user would land at a shell
inside a sandbox missing its install: side effects with no signal
that anything went wrong.
"""
from __future__ import annotations

import pytest

from helpers import sbx
from helpers.contract import minimal_kit, sbx_run_until_exit

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(120)
def test_install_failure_surfaces_loud(sandbox_name):
    # `false` always exits 1. Sandbox must NOT come up; sbx run must exit non-zero.
    rc, stderr = sbx_run_until_exit(
        minimal_kit(install=["false"]),
        sandbox_name,
    )
    assert rc != 0, (
        f"sbx run should exit non-zero when install: returns non-zero; "
        f"got rc={rc}\nstderr={stderr}"
    )
    assert not sbx.sandbox_exists(sandbox_name), (
        f"failed install must not leave a sandbox behind; "
        f"`sbx ls` still shows it as {sbx.sandbox_state(sandbox_name)!r}"
    )
