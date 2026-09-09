"""189: state watchdog respawns the mutagen daemon after an out-of-band kill.

The mutagen daemon is daemon-wide (not per-project). Kill its process,
verify the state watchdog notices within its tick and respawns it.
Also verifies the daemon error log carries the drift-detection line.
"""
from __future__ import annotations

import os
import signal
import subprocess
import time
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


_MUTAGEN_LOCK = str(
    Path.home() / "Library" / "Application Support" / "devm-e2e"
    / "mutagen" / "data" / "daemon" / "daemon.lock"
)


def _mutagen_daemon_pid() -> int | None:
    # ps identifies BOTH the prod and e2e mutagen daemons as
    # `mutagen daemon run` — they differ only by MUTAGEN_DATA_DIRECTORY,
    # invisible to ps. The lock file uniquely identifies the e2e daemon,
    # so lsof on it returns the right pid regardless of what other
    # mutagen processes exist. Same pattern as test_202.
    r = subprocess.run(["lsof", "-t", _MUTAGEN_LOCK], capture_output=True, text=True, timeout=10)
    if r.returncode != 0:
        return None
    pid = r.stdout.strip().split("\n")[0]
    return int(pid) if pid else None


@pytest.mark.timeout(600)
def test_watchdog_respawns_mutagen_daemon(devm, workspace):
    workspace.write_devmyaml()
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=180)
    assert r.returncode == 0, r.stderr.decode()
    try:
        pid = _mutagen_daemon_pid()
        assert pid is not None, "mutagen daemon should be running"

        os.kill(pid, signal.SIGKILL)
        time.sleep(2)
        assert _mutagen_daemon_pid() is None

        deadline = time.monotonic() + 90
        respawned_pid = None
        while time.monotonic() < deadline:
            new_pid = _mutagen_daemon_pid()
            if new_pid is not None and new_pid != pid:
                respawned_pid = new_pid
                break
            time.sleep(2)
        assert respawned_pid is not None, "watchdog did not respawn mutagen within 90s"

        log_path = Path.home() / "Library" / "Logs" / "com.devm.e2e.service.err.log"
        log_content = log_path.read_text(errors="replace")
        assert "watchdog: drift on mutagen" in log_content, (
            f"expected watchdog drift log for mutagen; last 30 lines:\n"
            f"{''.join(log_content.splitlines(keepends=True)[-30:])}"
        )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"],
                       cwd=str(workspace.path), timeout=60, capture_output=True)
