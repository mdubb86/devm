"""65: install step N+1 sees step N's filesystem effects.

Pins that install: runs sequentially with completion ordering — step
N+1 starts only after step N has FINISHED, so it can rely on step N's
filesystem side effects.

Setup:
  - install step 1: touch /tmp/from-step-1
  - install step 2: test -f /tmp/from-step-1  (fails if step 1 didn't
    finish first)

The tart_sandbox fixture cold-starts via `devm shell -- true`. If
install steps ran in parallel or with overlap, step 2 would race step 1
and the cold-start would fail (returning a non-running VM). Reaching
running state itself proves both steps completed in order.

Devm dependency: install: is ordered + fail-fast; users may write step
N+1 to depend on step N's side effects. If devm ever parallelizes
install, test_65 fires.
"""
from __future__ import annotations

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_install_step_2_sees_step_1_effects(workspace, devm, tart_sandbox):
    workspace.write_devmyaml(
        install=[
            "touch /tmp/from-step-1",
            # If step 2 starts before step 1 finishes, this fails and
            # devm shell exits non-zero (loud failure per test_51).
            "test -f /tmp/from-step-1",
        ],
    )

    # tart_sandbox fixture cold-starts. If we reach here the VM is up,
    # meaning both install steps succeeded in order.
    assert tart_sandbox.state() == "running", (
        f"expected VM running after ordered install; got {tart_sandbox.state()!r}"
    )

    # Belt-and-suspenders: confirm the marker is still on disk.
    r = tart_sandbox.exec_shell("test -f /tmp/from-step-1 && echo present")
    assert r.ok and "present" in r.stdout, (
        f"/tmp/from-step-1 missing post-bringup; install ordering or "
        f"completion semantics changed. stdout={r.stdout!r} stderr={r.stderr!r}"
    )
