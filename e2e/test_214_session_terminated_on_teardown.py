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


_ON_TEARDOWN_DEVM_SH = """#!/usr/bin/env bash
set -eo pipefail
backup_appstate()  { echo on-teardown-marker; }
restore_appstate() { cat > /dev/null; }
"""


@pytest.mark.timeout(900)
def test_on_teardown_captures_snapshot_before_teardown(workspace, devm, sandbox_name):
    """on_teardown:true runs the backup hook as part of `devm teardown`
    and one archive lands on the Mac side before the VM is destroyed."""
    import glob
    from pathlib import Path

    workspace.write_devmyaml(
        no_repo=True,
        snapshots={
            "appstate": {"on_teardown": True},
        },
    )
    workspace.write_devm_sh(_ON_TEARDOWN_DEVM_SH)

    r = subprocess.run(
        [devm.path, "start"], cwd=str(workspace.path),
        capture_output=True, timeout=600,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    devm.teardown(yes=True, timeout=120)

    archive_dir = (
        Path.home()
        / "Library" / "Application Support" / "devm-e2e"
        / workspace.vm_name / "snapshots" / "appstate"
    )
    matches = glob.glob(str(archive_dir / "*.zst"))
    assert len(matches) >= 1, (
        f"expected on_teardown snapshot archive under {archive_dir}, got {matches}"
    )
