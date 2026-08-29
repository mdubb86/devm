"""Pin: `mutagen sync terminate` removes the session — sync list no
longer returns it.

devm's TeardownPhase (internal/serviceapi/mutagen_sessions.go) calls
sync terminate on every session belonging to a torn-down project. If a
terminated session lingered in sync list, the next `devm start` for the
same project would try to SyncCreate under the same name and fail with
'a session already exists'. Devm's cleanup contract depends on this.
"""
from __future__ import annotations

import shutil
import tempfile
from pathlib import Path

import pytest

from helpers import mutagen_contract as mc

pytestmark = pytest.mark.contract


def test_terminate_removes_session():
    alpha = Path(tempfile.mkdtemp(prefix="mut-alpha-", dir="/tmp"))
    beta = Path(tempfile.mkdtemp(prefix="mut-beta-", dir="/tmp"))
    try:
        with mc.daemon() as data_dir:
            mc.run(
                ["sync", "create", "--name", "terminate-test", str(alpha), str(beta)],
                data_dir=data_dir,
            )
            rows = mc.sync_list(data_dir)
            assert len(rows) == 1
            session_id = rows[0]["identifier"]

            r = mc.run(["sync", "terminate", session_id], data_dir=data_dir)
            assert r.returncode == 0, f"sync terminate failed: {r.stderr!r}"

            after = mc.sync_list(data_dir)
            assert after == [], (
                f"expected empty session list after terminate; got {after!r}. "
                f"A lingering terminated session would collide with the next "
                f"SyncCreate under the same name (devm's TeardownPhase -> "
                f"next SetupPhase pattern)."
            )
    finally:
        shutil.rmtree(alpha, ignore_errors=True)
        shutil.rmtree(beta, ignore_errors=True)
