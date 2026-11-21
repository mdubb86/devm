"""251: killing the Mac-side mutagen daemon surfaces in devm status
within ~5s via the mutagen sync monitor subscription (Task 9), NOT
via the 60s watchdog poll.

Stimulus is a real, targeted kill of mutagen's daemon process. The
original brief specified `systemctl stop sshd` inside the guest, but
devm's mutagen transport is the `tart-mutagen-ssh` shim over `tart
exec`, not real SSH — so sshd doesn't matter to mutagen. Killing the
Mac-side mutagen daemon directly breaks every sync session for every
project in one call.

The PID is resolved via `lsof` on the daemon's own lock file (as
internal/serviceapi/mutagen.go's mutagenLockPID and e2e test_202 both
do), not by guessing at the extracted binary's path — lsof on the
e2e identity's own data dir is exact, and killing by PID (rather than
`pkill -f <path>`) can't accidentally match an unrelated process.

Also verifies the recovery path: the mutagen watchdog check
(watchdog_check_mutagen.go) respawns the daemon on its next tick, and
Task 9's subscriber reconnects to it, restoring MutagenHealth to ok.
"""
from __future__ import annotations
import json
import os
import signal
import subprocess
import time

import pytest

pytestmark = pytest.mark.devm

_LOCK_PATH = os.path.expanduser(
    "~/Library/Application Support/devm-e2e/mutagen/data/daemon/daemon.lock"
)


def _mutagen_pid() -> int | None:
    r = subprocess.run(["lsof", "-t", _LOCK_PATH], capture_output=True, text=True)
    out = r.stdout.strip()
    if not out:
        return None
    return int(out.splitlines()[0])


def _status_json(devm_path, cwd):
    r = subprocess.run(
        [devm_path, "status", "--json"],
        cwd=cwd, capture_output=True, timeout=10,
    )
    if r.returncode != 0:
        return None
    try:
        return json.loads(r.stdout.decode())
    except Exception:
        return None


def _current_mutagen_health(devm_path, cwd):
    j = _status_json(devm_path, cwd)
    if j is None:
        return None
    return j.get("project", {}).get("mutagen_health", "")


@pytest.mark.slow
@pytest.mark.timeout(300)
def test_mutagen_daemon_death_surfaces_within_seconds(devm, workspace, sandbox_name):
    workspace.write_devmyaml()  # repo-having so mutagen sessions exist
    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"cold-start failed: {r.stderr.decode()!r}"

        # Baseline: give the mutagen monitor subscriber a moment to
        # observe initial state, then confirm ok.
        time.sleep(3)
        health = _current_mutagen_health(devm.path, str(workspace.path))
        assert health == "ok", f"baseline mutagen_health should be ok, got {health!r}"

        # Stimulus: kill the Mac-side mutagen daemon by PID, resolved
        # from the e2e identity's own daemon.lock — scoped precisely
        # to devm-e2e's mutagen, never a user's own.
        old_pid = _mutagen_pid()
        assert old_pid is not None, f"no process holds {_LOCK_PATH!r} — mutagen daemon not running"
        os.kill(old_pid, signal.SIGKILL)

        # Poll status up to 15s waiting for mutagen_health to flip to dead.
        deadline = time.monotonic() + 15
        saw_dead = False
        while time.monotonic() < deadline:
            health = _current_mutagen_health(devm.path, str(workspace.path))
            if health == "dead":
                saw_dead = True
                break
            time.sleep(0.5)
        assert saw_dead, (
            f"mutagen monitor didn't surface daemon death within 15s "
            f"(last observed health: {health!r})"
        )

        # Recovery: watchdog respawns mutagen (~60s tick); subscriber
        # reconnects and observes state again. Allow up to 90s.
        deadline = time.monotonic() + 90
        recovered = False
        while time.monotonic() < deadline:
            health = _current_mutagen_health(devm.path, str(workspace.path))
            if health == "ok":
                recovered = True
                break
            time.sleep(1)
        assert recovered, (
            f"mutagen_health didn't recover to ok within 90s of daemon "
            f"death (last: {health!r})"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"], cwd=str(workspace.path),
            capture_output=True, timeout=60,
        )
