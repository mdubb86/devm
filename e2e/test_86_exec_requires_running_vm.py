"""86: `devm exec` fails loud when the sandbox has never been provisioned.

Matches the docker exec / kubectl exec convention: exec is a run-in-a-
running-thing operation, not an implicit start. If the sandbox is
absent, exec must fail with a clear, actionable error — never silently
trigger a 90-second cold start when the caller thought they were doing
a quick check.

The VM-stopped variant formerly here is now pinned in test_84, as the
final step of the same boot that already proves `devm start` and
`devm exec` — no need for a second VM just to re-check the identical
"not running" failure mode from a different starting state (absent vs.
stopped both exercise the same guard in orchestrator code).

What this pins:
  - `devm exec ...` on an absent VM returns non-zero.
  - Error message points the user at `devm start` / `devm shell`.

What it doesn't cover (tested elsewhere):
  - `devm exec ...` on a stopped VM -> test_84.
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
