"""228: project.name colliding with a devm-internal storage dir is rejected.

Task 7 (mutagen-volumes plan) added `validateProjectIDReserved`, which
rejects a project.name equal to one of devm's internal storage
directory names (`bin`, `state`, `iron-proxy`, `mutagen`, `volumes`).
A project using one of these names would shadow devm's own storage
layout under the daemon's Application Support root.

Validation-only: `devm reconcile` fails at `config.Load`, before the
CLI ever talks to the VM or the daemon's reconcile endpoint, so no
sandbox is created. The passing half (an unrelated project.name) does
go on to talk to the e2e daemon, same as any other `devm reconcile` on
a project that's never been started.

Pins:
1. `project.name: mutagen` fails `devm reconcile`; the error names the
   reserved word and the reason (collides with devm's internal
   storage layout).
2. An unrelated project.name passes.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(60)
def test_projectid_reserved_name_rejected_then_resolved(devm, workspace):
    workspace.write_devmyaml(no_repo=True, project={"name": "mutagen"})

    r = subprocess.run(
        [devm.path, "reconcile"], cwd=str(workspace.path),
        capture_output=True, timeout=30,
    )
    assert r.returncode != 0, (
        "reconcile should reject project.name: mutagen (a reserved "
        f"devm-internal storage dir name); got exit 0. "
        f"stdout={r.stdout.decode()!r}"
    )
    err = r.stderr.decode()
    assert '"mutagen"' in err, f"error should name the offending value; got:\n{err}"
    assert "devm-internal storage dir" in err, (
        f"error should state the reserved-name reason; got:\n{err}"
    )

    # An unrelated project.name passes. Reuse the fixture's unique
    # vm_name (already guaranteed unrelated to any reserved word).
    workspace.write_devmyaml(no_repo=True)

    r = subprocess.run(
        [devm.path, "reconcile"], cwd=str(workspace.path),
        capture_output=True, timeout=30,
    )
    assert r.returncode == 0, (
        "reconcile should accept a project.name that isn't reserved: "
        f"stdout={r.stdout.decode()!r} stderr={r.stderr.decode()!r}"
    )
