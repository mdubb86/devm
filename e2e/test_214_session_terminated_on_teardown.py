"""214: `devm teardown` permanently terminates every mutagen session
belonging to the project.

TeardownPhase (internal/serviceapi/mutagen_sessions.go) calls
`SyncTerminate` on every session under the project's name prefix --
unlike StopPhase's pause, this is not resumable.
"""
from __future__ import annotations
import subprocess

import pytest

from helpers.mutagen_e2e import session_prefix, sync_list

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_session_terminated_on_teardown(devm, workspace):
    workspace.write_devmyaml()  # default repos.main only
    prefix = session_prefix(workspace.vm_name)

    r = subprocess.run(
        [devm.path, "start"], cwd=str(workspace.path),
        capture_output=True, timeout=180,
    )
    assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

    sessions = sync_list(prefix)
    assert len(sessions) == 1, f"expected exactly one session before teardown, got {sessions}"

    r = subprocess.run(
        [devm.path, "teardown", "--yes"], cwd=str(workspace.path),
        capture_output=True, timeout=60,
    )
    assert r.returncode == 0, f"devm teardown failed:\n{r.stderr.decode()}"

    sessions = sync_list(prefix)
    assert sessions == [], (
        f"expected no sessions with prefix {prefix!r} after teardown, got {sessions}"
    )
