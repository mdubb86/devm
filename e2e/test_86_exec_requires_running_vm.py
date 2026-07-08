"""86: `devm exec` fails loud when the sandbox isn't running.

Matches the docker exec / kubectl exec convention: exec is a run-in-a-
running-thing operation, not an implicit start. If the sandbox is
absent or stopped, exec must fail with a clear, actionable error —
never silently trigger a 90-second cold start when the caller thought
they were doing a quick check.

Two variants:
  - VM absent (never provisioned): fail.
  - VM stopped (previously provisioned, `devm stop` called): fail.

What this pins:
  - `devm exec ...` on an absent VM returns non-zero.
  - `devm exec ...` on a stopped VM returns non-zero.
  - Error message points the user at `devm start` / `devm shell`.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(60)
def test_exec_fails_when_vm_absent(workspace, devm):
    """No prior `devm start` — the VM was never provisioned. `devm exec`
    must fail without cold-starting."""
    workspace.write_devmyaml()

    p = subprocess.run(
        [devm.path, "exec", "true"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=30,
    )
    assert p.returncode != 0, (
        f"devm exec on absent VM should fail; got rc=0\n"
        f"stdout={p.stdout.decode()!r}\nstderr={p.stderr.decode()!r}"
    )
    err = p.stderr.decode()
    assert "not running" in err, (
        f"expected 'not running' in error; got:\n{err}"
    )
    # The message should point the user at the recovery command.
    assert "devm start" in err or "devm shell" in err, (
        f"expected 'devm start' or 'devm shell' hint in error; got:\n{err}"
    )


@pytest.mark.timeout(240)
def test_exec_fails_when_vm_stopped(workspace, devm):
    """VM provisioned then stopped — exec must still fail loud, not
    silently cold-start a warm sandbox."""
    workspace.write_devmyaml()

    start = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=180,
    )
    assert start.returncode == 0, (
        f"devm start failed: rc={start.returncode}\n"
        f"stderr={start.stderr.decode()!r}"
    )

    stop = subprocess.run(
        [devm.path, "stop", "--yes"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=60,
    )
    assert stop.returncode == 0, (
        f"devm stop failed: rc={stop.returncode}\n"
        f"stderr={stop.stderr.decode()!r}"
    )

    p = subprocess.run(
        [devm.path, "exec", "true"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=30,
    )
    assert p.returncode != 0, (
        f"devm exec on stopped VM should fail; got rc=0\n"
        f"stdout={p.stdout.decode()!r}\nstderr={p.stderr.decode()!r}"
    )
    err = p.stderr.decode()
    assert "not running" in err, (
        f"expected 'not running' in error; got:\n{err}"
    )
