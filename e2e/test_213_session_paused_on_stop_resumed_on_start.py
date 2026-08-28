"""213: `devm stop` pauses the project's mutagen session; `devm start`
resumes the SAME session (not a fresh one).

StopPhase (internal/serviceapi/mutagen_sessions.go) flushes then
pauses every session under the project's name prefix. SetupPhase's
resume branch (`if existing.Status == "paused" { SyncResume }`) picks
the paused session back up on the next start rather than terminating
and recreating it.
"""
from __future__ import annotations
import subprocess

import pytest

from helpers.mutagen_e2e import session_prefix, sync_list

pytestmark = pytest.mark.devm


@pytest.mark.timeout(240)
def test_session_paused_on_stop_resumed_on_start(devm, workspace):
    workspace.write_devmyaml()  # default repos.main only
    prefix = session_prefix(workspace.vm_name)

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

        sessions = sync_list(prefix)
        assert len(sessions) == 1, f"expected exactly one session, got {sessions}"
        session_id = sessions[0]["identifier"]

        r = subprocess.run(
            [devm.path, "stop", "--yes"], cwd=str(workspace.path),
            capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"devm stop failed:\n{r.stderr.decode()}"

        sessions = sync_list(prefix)
        assert len(sessions) == 1, f"expected the same one session after stop, got {sessions}"
        assert sessions[0]["identifier"] == session_id, (
            f"session identifier changed across stop: {session_id} -> {sessions[0]['identifier']}"
        )
        # Mutagen exposes user-pause state in `paused` (bool), not
        # `status` (transport/activity state — Watching, Disconnected,
        # etc.). Pinned in e2e/test_mutagen_contract_04 + 05.
        assert sessions[0]["paused"] is True, (
            f"expected paused=true after devm stop, got "
            f"paused={sessions[0].get('paused')!r} status={sessions[0]['status']!r}"
        )

        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"devm start (resume) failed:\n{r.stderr.decode()}"

        sessions = sync_list(prefix)
        assert len(sessions) == 1, f"expected the same one session after resume, got {sessions}"
        assert sessions[0]["identifier"] == session_id, (
            f"session identifier changed across resume: {session_id} -> {sessions[0]['identifier']} "
            f"-- expected a resume of the SAME session, not a fresh create"
        )
        assert sessions[0]["paused"] is False, (
            f"expected paused=false after devm start resumed it, got "
            f"paused={sessions[0].get('paused')!r} status={sessions[0]['status']!r}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
