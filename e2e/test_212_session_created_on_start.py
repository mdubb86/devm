"""212: `devm start` creates exactly one mutagen sync session for the
project's primary repo, named `devm-<projectID>-<label>`.

SessionName (internal/serviceapi/mutagen_sessions.go) fixes this
shape. With no other `repos:`/`volumes:` entries, BuildEntities
produces exactly one SessionEntity (the primary), so exactly one
session should exist under the project's name prefix.
"""
from __future__ import annotations
import subprocess

import pytest

from helpers.mutagen_e2e import session_name, session_prefix, sync_list

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_session_created_on_start(devm, workspace):
    workspace.write_devmyaml()  # default repos.main only
    label = f"{workspace.path.name}-repo"  # BareCloneName of bare_repo_url()

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

        sessions = sync_list(session_prefix(workspace.vm_name))
        expected_name = session_name(workspace.vm_name, label)
        names = [s["name"] for s in sessions]
        assert names == [expected_name], (
            f"expected exactly one session named {expected_name!r}, got {names!r}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
