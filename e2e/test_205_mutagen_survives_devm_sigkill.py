"""205: mutagen sessions restore after a SIGKILL of the devm-e2e daemon.

Unlike test_204 (a deliberate bootout+bootstrap), this reproduces the
ungraceful case: SIGKILL the devm-e2e daemon PID directly. The
LaunchDaemon plist has KeepAlive=true, so launchd relaunches it
automatically with no explicit action from this test. The mutagen
daemon (a direct child, no setsid shim — see test_200) dies alongside
its SIGKILLed parent; the new devm-e2e daemon's startup path
(AdoptMutagenDaemon / SpawnMutagen) spawns a fresh mutagen daemon
against the SAME MUTAGEN_DATA_DIRECTORY, which restores the prior
session set from its on-disk database.

Marked `install`: SIGKILLing the shared e2e daemon and waiting on
launchd's relaunch is disruptive to any other test sharing the
daemon mid-run, same class of risk as test_204's launchctl dance.
"""
from __future__ import annotations
import os
import signal
import subprocess
import time

import pytest

from helpers.mutagen_e2e import session_prefix, sync_list

pytestmark = pytest.mark.install


def _daemon_pid(devm_path: str) -> int | None:
    r = subprocess.run(["pgrep", "-f", f"{devm_path} serve"], capture_output=True, text=True)
    if r.returncode != 0:
        return None
    lines = r.stdout.strip().split()
    return int(lines[0]) if lines else None


_SOCK_PATH = "/Users/michael/Library/Application Support/devm-e2e/devm.sock"


def _wait_socket_present(timeout: float = 30.0) -> None:
    import pathlib
    p = pathlib.Path(_SOCK_PATH)
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if p.exists():
            return
        time.sleep(0.1)
    raise AssertionError(f"devm-e2e socket never appeared at {_SOCK_PATH} within {timeout}s")




@pytest.mark.timeout(180)
def test_mutagen_survives_devm_sigkill(devm, devm_path, workspace):
    # The prior install-marker test (test_204) leaves the shared daemon
    # in a state where its socket file is briefly missing — the daemon
    # process itself is alive but its /Users/…/devm-e2e/devm.sock file
    # doesn't exist for a window that overlaps this test's very first
    # `devm start`. `devm status` (which the sibling _wait_daemon_up
    # helper uses) reports rc=0 in that state, so poll the socket file
    # directly instead.
    _wait_socket_present()

    r = subprocess.run(
        [devm.path, "start"], cwd=str(workspace.path),
        capture_output=True, timeout=180,
    )
    assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

    prefix = session_prefix(workspace.vm_name)
    before = sync_list(prefix)
    assert len(before) >= 1, f"expected at least one session for {prefix!r}, got {before}"
    ids_before = sorted(s["identifier"] for s in before)

    try:
        pid = _daemon_pid(devm_path)
        assert pid is not None, "devm-e2e daemon PID not found via pgrep"

        os.kill(pid, signal.SIGKILL)

        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            try:
                os.kill(pid, 0)
            except ProcessLookupError:
                break
            time.sleep(0.2)
        else:
            pytest.fail(f"daemon (pid {pid}) still alive 10s after SIGKILL")

        _wait_socket_present(timeout=30)

        deadline = time.monotonic() + 10
        after: list[dict] = []
        while time.monotonic() < deadline:
            after = sync_list(prefix)
            if sorted(s["identifier"] for s in after) == ids_before:
                break
            time.sleep(0.5)

        ids_after = sorted(s["identifier"] for s in after)
        assert ids_after == ids_before, (
            f"mutagen sessions did not restore within 10s of the devm-e2e daemon's "
            f"relaunch: before={ids_before} after={ids_after}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
