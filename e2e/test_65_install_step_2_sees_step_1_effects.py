"""65: install step N+1 sees step N's filesystem effects.

Pins that install: runs sequentially with completion ordering — step
N+1 starts only after step N has FINISHED, so it can rely on step N's
filesystem side effects.

Setup:
  - install step 1: touch /tmp/from-step-1
  - install step 2: test -f /tmp/from-step-1  (fails if step 1 didn't
    finish first)

The first `devm shell -- true` IS the cold-start that runs install
steps in order. If steps ran in parallel or with overlap, step 2 would
race step 1 and the cold-start would fail. A successful cold-start
(rc=0) proves both steps completed in order.

Devm dependency: install: is ordered + fail-fast; users may write step
N+1 to depend on step N's side effects. If devm ever parallelizes
install, test_65 fires.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_install_step_2_sees_step_1_effects(workspace, devm):
    workspace.write_devmyaml(
        install=[
            "touch /tmp/from-step-1",
            # If step 2 starts before step 1 finishes, this fails and
            # devm shell exits non-zero (loud failure per test_51).
            "test -f /tmp/from-step-1",
        ],
    )

    # First devm shell IS the cold-start; rc=0 means both steps ran in order.
    p = subprocess.run(
        [devm.path, "shell", "--", "true"],
        capture_output=True, cwd=str(workspace.path), timeout=180,
    )
    assert p.returncode == 0, (
        f"cold-start with sequential install steps failed; ordering invariant broken.\n"
        f"stdout={p.stdout.decode()!r}\nstderr={p.stderr.decode()!r}"
    )

    # Belt-and-suspenders: confirm the marker is still on disk in the VM.
    r = subprocess.run(
        [devm.path, "shell", "--", "bash", "-c",
         "test -f /tmp/from-step-1 && echo present"],
        capture_output=True, cwd=str(workspace.path), timeout=60,
    )
    assert r.returncode == 0 and "present" in r.stdout.decode(), (
        f"/tmp/from-step-1 missing post-bringup; install ordering or "
        f"completion semantics changed. stdout={r.stdout.decode()!r} stderr={r.stderr.decode()!r}"
    )
