"""Pin: `.paused == true` survives `mutagen daemon stop` + `mutagen
daemon start`.

If a paused session came back as paused=false on daemon restart, devm's
AdoptMutagenDaemon respawn would silently resume every project's
sessions — including the ones the user just paused with `devm stop`.
This test locks the opposite: paused stays paused across the restart,
so `devm start` after `devm stop` sees paused=true and correctly calls
sync resume rather than treating the session as already-active.

Symmetric with contract 12 (identifier preservation).
"""
from __future__ import annotations

import shutil
import tempfile
import time
from pathlib import Path

import pytest

from helpers import mutagen_contract as mc

pytestmark = pytest.mark.contract


def _wait_lock(lock: Path, want_present: bool, timeout: float = 5.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if lock.exists() == want_present:
            return
        time.sleep(0.1)


def test_daemon_restart_preserves_paused_state():
    alpha = Path(tempfile.mkdtemp(prefix="mut-alpha-", dir="/tmp"))
    beta = Path(tempfile.mkdtemp(prefix="mut-beta-", dir="/tmp"))
    try:
        with mc.short_data_dir() as data_dir:
            lock = data_dir / "daemon" / "daemon.lock"

            mc.run(["daemon", "start"], data_dir=data_dir)
            _wait_lock(lock, want_present=True)

            mc.run(
                ["sync", "create", "--name", "paused-persist",
                 str(alpha), str(beta)],
                data_dir=data_dir,
            )
            session_id = mc.sync_list(data_dir)[0]["identifier"]

            mc.run(["sync", "pause", session_id], data_dir=data_dir)
            assert mc.sync_list(data_dir)[0]["paused"] is True, (
                "pause step must land before daemon restart"
            )

            # Restart the daemon around the paused session.
            mc.run(["daemon", "stop"], data_dir=data_dir)
            _wait_lock(lock, want_present=False)
            mc.run(["daemon", "start"], data_dir=data_dir)
            _wait_lock(lock, want_present=True)

            try:
                after = mc.sync_list(data_dir)
                assert len(after) == 1
                assert after[0]["identifier"] == session_id
                assert after[0]["paused"] is True, (
                    f"paused=true did NOT survive daemon restart. If this "
                    f"becomes false, devm's AdoptMutagenDaemon respawn "
                    f"would silently resume sessions the user just "
                    f"paused with `devm stop`. Got row: {after[0]!r}"
                )
            finally:
                mc.run(["daemon", "stop"], data_dir=data_dir, check=False)
    finally:
        shutil.rmtree(alpha, ignore_errors=True)
        shutil.rmtree(beta, ignore_errors=True)
