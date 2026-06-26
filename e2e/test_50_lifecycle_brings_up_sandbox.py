"""50: cold-start brings VM to running state and exec-ready.

Canonical baseline. A project with a no-op config (default devm.yaml)
must reach 'running' state after `devm shell -- true`. Every other
lifecycle test implicitly relies on this; if this breaks, almost
everything in the cohort will fail too.

What this pins:
  - Cold-create path brings the VM to 'running' state.
  - tart exec (via tart_sandbox.exec) works on the running VM.

What it doesn't cover (tested elsewhere):
  - Interactive shell prompt -> test_01.
  - Stop lifecycle -> test_03, test_52.
  - Teardown -> test_05, test_53.
"""
from __future__ import annotations

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(120)
def test_cold_start_brings_vm_to_running(tart_sandbox):
    # tart_sandbox fixture already cold-started the VM.
    assert tart_sandbox.state() == "running", (
        f"expected VM to be running after cold-start; got {tart_sandbox.state()!r}"
    )
    # Exec-ready: a no-op exec should succeed.
    result = tart_sandbox.exec("true")
    assert result.exit_code == 0, (
        f"`true` inside VM should return 0; got {result.exit_code}"
    )
