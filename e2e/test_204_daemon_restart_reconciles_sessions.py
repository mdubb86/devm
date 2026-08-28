"""204: mutagen sessions survive a devm-e2e daemon bootout+bootstrap.

Task 12/16.5's mutagen daemon is a direct child of the devm daemon
with no setsid shim (see test_200's docstring) — it dies when the
devm daemon's launchd job is torn down. But mutagen's own session
database lives on disk under MUTAGEN_DATA_DIRECTORY, independent of
any particular daemon process: a freshly spawned mutagen daemon
pointed at the same data dir reads the same sessions back.

This pins that a full `launchctl bootout` + `launchctl bootstrap` of
the e2e LaunchDaemon (a stronger action than `devm service restart`,
which does the same thing internally but leaves no window where NO
daemon is registered) still ends with the same mutagen session
identifiers present afterward.

Mutates the shared e2e daemon's launchd registration -> `install`
marker (single-process phase, `just e2e-install`).
"""
from __future__ import annotations
import subprocess
import time

import pytest

from helpers.mutagen_e2e import session_prefix, sync_list

pytestmark = pytest.mark.install

_TARGET = "system/com.devm.e2e.service"
_PLIST = "/Library/LaunchDaemons/com.devm.e2e.service.plist"


def _sock_path() -> str:
    import os
    return os.path.expanduser("~/Library/Application Support/devm-e2e/devm.sock")


def _wait_daemon_up(devm_path: str, timeout: float = 30.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        r = subprocess.run([devm_path, "status"], capture_output=True, timeout=10)
        if r.returncode == 0:
            return
        time.sleep(0.5)
    raise AssertionError(f"devm-e2e daemon never came back up within {timeout}s")


@pytest.mark.timeout(240)
def test_daemon_restart_reconciles_sessions(devm, devm_path, workspace):
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
        r = subprocess.run(["sudo", "launchctl", "bootout", _TARGET], capture_output=True, timeout=30)
        # bootout can legitimately race with an already-exiting job; only
        # fail loud on an unexpected error shape.
        assert r.returncode == 0 or b"No such process" in r.stderr, (
            f"launchctl bootout {_TARGET} failed: {r.stderr.decode()!r}"
        )

        r = subprocess.run(
            ["sudo", "launchctl", "bootstrap", "system", _PLIST],
            capture_output=True, timeout=30,
        )
        assert r.returncode == 0, f"launchctl bootstrap {_PLIST} failed: {r.stderr.decode()!r}"

        _wait_daemon_up(devm_path)

        after = sync_list(prefix)
        ids_after = sorted(s["identifier"] for s in after)
        assert ids_after == ids_before, (
            f"mutagen session identifiers changed across daemon bootout+bootstrap: "
            f"before={ids_before} after={ids_after}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
