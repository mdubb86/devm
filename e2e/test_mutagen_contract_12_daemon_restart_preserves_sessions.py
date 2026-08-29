"""Pin: sessions persist across `mutagen daemon stop` + `mutagen daemon
start`. Same identifier, same name.

devm's AdoptMutagenDaemon (internal/serviceapi/mutagen.go) stops and
respawns the mutagen daemon on every devm-daemon start so the fresh
mutagen inherits the current build's env (HOME, MUTAGEN_DATA_DIRECTORY).
That model only works if mutagen persists its sessions in the DataDir
across restart — otherwise every devm boot loses every project's
mutagen state.

Complements contract 01 (daemon lifecycle mechanics). Together they lock
'we can respawn the daemon whenever we want and users don't notice'.
"""
from __future__ import annotations

import shutil
import tempfile
import time
from pathlib import Path

import pytest

from helpers import mutagen_contract as mc

pytestmark = pytest.mark.contract


def test_daemon_restart_preserves_sessions():
    alpha = Path(tempfile.mkdtemp(prefix="mut-alpha-", dir="/tmp"))
    beta = Path(tempfile.mkdtemp(prefix="mut-beta-", dir="/tmp"))
    try:
        with mc.short_data_dir() as data_dir:
            # First daemon lifecycle: start, create session, snapshot,
            # stop cleanly.
            mc.run(["daemon", "start"], data_dir=data_dir)
            # daemon.lock lag: mc.daemon() polls; we're managing manually.
            lock = data_dir / "daemon" / "daemon.lock"
            for _ in range(50):
                if lock.exists():
                    break
                time.sleep(0.1)
            assert lock.exists(), (
                "daemon didn't start on first attempt; see contract 01"
            )

            mc.run(
                ["sync", "create", "--name", "restart-test",
                 str(alpha), str(beta)],
                data_dir=data_dir,
            )
            before = mc.sync_list(data_dir)
            assert len(before) == 1
            before_id = before[0]["identifier"]
            before_name = before[0]["name"]

            mc.run(["daemon", "stop"], data_dir=data_dir)
            # Wait for the lock to release, mirroring devm's supervision.
            for _ in range(50):
                if not lock.exists():
                    break
                time.sleep(0.1)

            # Second daemon lifecycle: start, list, assert same session.
            mc.run(["daemon", "start"], data_dir=data_dir)
            for _ in range(50):
                if lock.exists():
                    break
                time.sleep(0.1)
            after = mc.sync_list(data_dir)
            try:
                assert len(after) == 1, (
                    f"expected the same one session after daemon restart; "
                    f"got {after!r}"
                )
                assert after[0]["identifier"] == before_id, (
                    f"session identifier changed across daemon restart. "
                    f"before={before_id!r} after={after[0]['identifier']!r} "
                    f"— every downstream CLI (sync flush/pause/resume by id) "
                    f"would break."
                )
                assert after[0]["name"] == before_name, (
                    f"session name changed across daemon restart. "
                    f"before={before_name!r} after={after[0]['name']!r}"
                )
            finally:
                mc.run(["daemon", "stop"], data_dir=data_dir, check=False)
    finally:
        shutil.rmtree(alpha, ignore_errors=True)
        shutil.rmtree(beta, ignore_errors=True)
