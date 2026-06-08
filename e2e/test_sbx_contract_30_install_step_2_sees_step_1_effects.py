"""install: step N+1 sees step N's filesystem effects.

Pins that install: runs sequentially with completion ordering — step
N+1 starts only after step N has FINISHED, so it can rely on step N's
filesystem side effects.

Setup:
  - install step 1: touch /tmp/from-step-1
  - install step 2: test -f /tmp/from-step-1  (fails with rc=1 if step 1
    didn't finish before step 2 started)

If sbx ran the steps in parallel or with overlap, step 2 would race
step 1 and likely fail (the marker wouldn't exist yet). Per contract_02,
a failing install: step makes sbx run exit non-zero and leaves no
sandbox — so the success of contract_sandbox (which waits for
running + exec-ready) itself proves both steps completed in order.

Devm dependency: internal design notes treats install: as ordered + fail-fast and lets
users put step dependencies in declaration order. If sbx ever changes
this, contract_30 fires.

Combined with contract_02 (install failure is loud), contract_30
pins the full "ordered, sequential, fail-fast" contract for install:.
"""
from __future__ import annotations

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(120)
def test_install_step_2_sees_step_1_effects(sandbox_name):
    spec = minimal_kit(
        install=[
            "touch /tmp/from-step-1",
            # If step 2 starts before step 1 finishes, this fails.
            # contract_02 then makes sbx run exit non-zero and
            # contract_sandbox raises during _wait_running.
            "test -f /tmp/from-step-1",
        ],
    )

    with contract_sandbox(spec, sandbox_name):
        # If we reach here, both steps succeeded in order. Belt-and-
        # suspenders: confirm the marker is still on disk.
        r = sbx_exec(sandbox_name, "test", "-f", "/tmp/from-step-1")
        assert r.returncode == 0, (
            f"/tmp/from-step-1 missing post-bringup; install ordering or "
            f"completion semantics changed. The startup-supervision "
            f"design's 'step N+1 can depend on step N' property no "
            f"longer holds for install:."
        )
