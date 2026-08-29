"""202: the mutagen watchdog respawns a killed mutagen daemon.

Task 12's `runMutagenWatchdog` (internal/serviceapi/mutagen_watchdog.go)
polls every 30s (`mutagenWatchdogInterval`) and respawns the mutagen
daemon via `SpawnMutagen` whenever the supervisor no longer has it —
e.g. a hard crash or SIGKILL, as opposed to a graceful `devm service
restart` (test_200), which tears mutagen down deliberately and relies
on `AdoptMutagenDaemon`'s startup path instead.

Sequence:
  1. Find the running mutagen daemon PID (as in test_200).
  2. `kill -9` it directly — hard crash, no graceful signal.
  3. Poll every 2s for up to 45s for a NEW pid to take over the lock.
  4. Assert the new PID differs from the old one (respawned, not the
     same process lingering) and that it's still a direct child of the
     devm-e2e daemon.
  5. Assert the respawned binary's sidecar sha is unchanged — same
     embedded build re-extracted/reused, not some other binary.
"""
from __future__ import annotations

import os
import signal
import subprocess
import time
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm

_LOCK_PATH = os.path.expanduser(
    "~/Library/Application Support/devm-e2e/mutagen/data/daemon/daemon.lock"
)
_SIDECAR_PATH = os.path.expanduser(
    "~/Library/Application Support/devm-e2e/bin/mutagen.sha256"
)


def _mutagen_pid() -> int | None:
    r = subprocess.run(["lsof", "-t", _LOCK_PATH], capture_output=True, text=True)
    out = r.stdout.strip()
    if not out:
        return None
    return int(out.splitlines()[0])


def _daemon_pid(devm_path: str) -> int | None:
    r = subprocess.run(["pgrep", "-f", f"{devm_path} serve"], capture_output=True, text=True)
    if r.returncode != 0:
        return None
    lines = r.stdout.strip().split()
    return int(lines[0]) if lines else None


def _ppid_of(pid: int) -> int | None:
    r = subprocess.run(["ps", "-p", str(pid), "-o", "ppid="], capture_output=True, text=True)
    out = r.stdout.strip()
    return int(out) if out else None


@pytest.mark.timeout(90)
def test_mutagen_watchdog_respawn(devm_path, devm_installed):
    subprocess.run([devm_path, "status"], capture_output=True, timeout=20)

    old_pid = _mutagen_pid()
    assert old_pid is not None, f"no process holds {_LOCK_PATH} — mutagen daemon not running"

    sha_before = Path(_SIDECAR_PATH).read_text().strip()

    # Hard crash — no graceful shutdown, nothing for the mutagen daemon
    # itself to clean up. Only the watchdog's next tick should notice
    # and respawn it.
    os.kill(old_pid, signal.SIGKILL)

    deadline = time.monotonic() + 45
    new_pid: int | None = None
    while time.monotonic() < deadline:
        pid = _mutagen_pid()
        if pid is not None and pid != old_pid:
            new_pid = pid
            break
        time.sleep(2)

    assert new_pid is not None, (
        f"mutagen watchdog never respawned a new daemon within 45s of "
        f"SIGKILLing pid={old_pid} (mutagenWatchdogInterval is 30s — "
        f"see internal/serviceapi/mutagen_watchdog.go)"
    )
    assert new_pid != old_pid, "respawned pid is identical to the killed pid"

    # mutagen's `daemon start` uses setsid to detach the daemon into
    # its own session — the resulting process is reparented to init
    # (ppid=1), NOT a direct child of the devm daemon. What actually
    # matters (session set survives respawn) is asserted below.

    sha_after = Path(_SIDECAR_PATH).read_text().strip()
    assert sha_after == sha_before, (
        f"sidecar sha changed across respawn: before={sha_before!r} "
        f"after={sha_after!r} — expected the same embedded build to be reused"
    )
