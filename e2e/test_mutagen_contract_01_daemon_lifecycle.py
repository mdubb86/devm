"""Pin: mutagen's daemon lifecycle — `daemon start`, `daemon.lock`, `daemon stop`.

devm depends on three specific mechanics for its own supervision of the
mutagen daemon (internal/serviceapi/mutagen.go, internal/mutagen/cli.go):

  1. `mutagen daemon start` returns 0 on success and forks/execs a
     detached daemon process that outlives the CLI invocation. Without
     that, SpawnMutagen would need its own daemonization scaffolding.

  2. That daemon holds an exclusive flock on
     `<MUTAGEN_DATA_DIRECTORY>/daemon/daemon.lock` for its lifetime.
     devm's mutagenLockPID uses `lsof -t` on this file to answer
     "which pid, if any, is the daemon?" — the check both adopt-across-
     restart (AdoptMutagenDaemon) and the mutagen watchdog rely on.

  3. `mutagen daemon stop` cleanly terminates the daemon and releases
     the lock. Without that, StopMutagen would have to send signals by
     pid.

If any of the three changes shape upstream, every one of devm's
mutagen supervision paths breaks silently.
"""
from __future__ import annotations

import subprocess
import time
from pathlib import Path

import pytest

from helpers import mutagen_contract as mc

pytestmark = pytest.mark.contract


def _lock_holder(lock_path: Path) -> int | None:
    """Return the pid holding lock_path, or None."""
    r = subprocess.run(
        ["lsof", "-t", str(lock_path)],
        capture_output=True, text=True, timeout=5,
    )
    if r.returncode != 0:
        return None
    line = r.stdout.strip().split()
    return int(line[0]) if line else None


def _pid_alive(pid: int) -> bool:
    try:
        subprocess.run(["ps", "-p", str(pid)], capture_output=True, check=True, timeout=5)
        return True
    except subprocess.CalledProcessError:
        return False


def test_mutagen_daemon_lifecycle():
    # Short-path DataDir: macOS Unix socket paths are capped at 104
    # bytes, and mutagen binds <DataDir>/daemon/daemon.sock. pytest's
    # tmp_path lives under a long /private/var/folders/... prefix that
    # blows past the cap, and mutagen silently fails to bind. Use /tmp
    # directly. See helpers.mutagen_contract.short_data_dir.
    with mc.short_data_dir() as data_dir:
        _run_daemon_lifecycle(data_dir)


def _run_daemon_lifecycle(data_dir: Path) -> None:
    # (1) start returns 0
    r = mc.run(["daemon", "start"], data_dir=data_dir)
    assert r.returncode == 0, f"daemon start failed: {r.stderr!r}"

    # (2) daemon.lock exists AT the path devm computes, and lsof reports
    # exactly one pid holding it — the daemon.
    #
    # `daemon start` returns to the CLI before the freshly-forked daemon
    # child has written its lock file — poll briefly. devm's own
    # supervision path has the same shape (SpawnMutagen returns the pid
    # via lsof after `daemon start` returns), so this timing is part of
    # the contract we depend on.
    lock_path = data_dir / "daemon" / "daemon.lock"
    for _ in range(50):  # up to 5s
        if lock_path.exists():
            break
        time.sleep(0.1)
    assert lock_path.exists(), (
        f"daemon.lock missing at {lock_path} 5s after `daemon start` "
        f"returned 0 — devm's mutagenLockPID would return 0 (no daemon) "
        f"and try to spawn a duplicate"
    )
    pid = _lock_holder(lock_path)
    assert pid is not None, (
        f"no process holds {lock_path} — daemon.lock isn't the flock devm "
        f"assumes"
    )
    assert _pid_alive(pid), f"lock-holding pid={pid} is dead"

    # Second `daemon start` is a no-op (same pid still holds the lock)
    # — devm's SpawnMutagen tolerates being called against a running
    # daemon by DELEGATING to `daemon start` and doing the lsof again,
    # which is only safe if the tool itself is idempotent.
    r = mc.run(["daemon", "start"], data_dir=data_dir)
    assert r.returncode == 0
    same_pid = _lock_holder(lock_path)
    assert same_pid == pid, (
        f"second `daemon start` produced a different holder — devm's "
        f"idempotent-start assumption breaks. was={pid} now={same_pid}"
    )

    # (3) daemon stop releases the lock and terminates the process.
    r = mc.run(["daemon", "stop"], data_dir=data_dir)
    assert r.returncode == 0, f"daemon stop failed: {r.stderr!r}"

    deadline = time.monotonic() + 5
    while time.monotonic() < deadline:
        if not _pid_alive(pid) and _lock_holder(lock_path) is None:
            break
        time.sleep(0.1)
    assert not _pid_alive(pid), (
        f"daemon pid={pid} still alive 5s after `daemon stop`"
    )
    assert _lock_holder(lock_path) is None, (
        f"{lock_path} still held after `daemon stop`"
    )
