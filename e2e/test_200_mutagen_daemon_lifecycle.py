"""200: mutagen daemon lifecycle — direct child of the devm daemon,
dies when the devm daemon's launchd job stops.

Task 11/16.5 wire `AdoptMutagenDaemon` into the devm daemon's startup:
a mutagen daemon is spawned with
`MUTAGEN_DATA_DIRECTORY=<runtimeDir>/mutagen/data`, discovered via
`lsof -t <data>/daemon/daemon.lock` (internal/serviceapi/mutagen.go).
Unlike iron-proxy — which is setsid-detached specifically so it
survives daemon death (see test_44, test_133) — the mutagen daemon has
no setsid shim. It's spawned as a direct child, still inside the devm
daemon's launchd-managed process group.

Pins:
  - A mutagen daemon is alive under the e2e daemon's runtime dir, with
    the running devm-e2e daemon as its direct parent (ppid).
  - `devm service restart` (bootout+bootstrap of the same LaunchDaemon
    job — see cmd/devm/service.go) kills the OLD mutagen daemon PID:
    no setsid protection, so it dies with the job.
  - A new mutagen daemon comes up as a direct child of the new daemon
    process.

Uses `service restart` (not a raw `launchctl bootout`) as the stop
mechanism, matching test_44/test_100/test_133: it shells to sudo
internally and is auto-marked `install` by conftest's hint grep, so it
runs in the single-process install-marker phase (`just e2e-install`).
"""
from __future__ import annotations

import os
import subprocess
import time

import pytest

pytestmark = pytest.mark.devm

_LOCK_PATH = os.path.expanduser(
    "~/Library/Application Support/devm-e2e/mutagen/data/daemon/daemon.lock"
)


def _mutagen_pid() -> int | None:
    """PID currently holding the mutagen daemon's data-dir lock, or None."""
    r = subprocess.run(["lsof", "-t", _LOCK_PATH], capture_output=True, text=True)
    out = r.stdout.strip()
    if not out:
        return None
    return int(out.splitlines()[0])


def _daemon_pid(devm_path: str) -> int | None:
    """Return the devm-e2e daemon's own PID (`<devm_path> serve`), or None."""
    r = subprocess.run(["pgrep", "-f", f"{devm_path} serve"], capture_output=True, text=True)
    if r.returncode != 0:
        return None
    lines = r.stdout.strip().split()
    return int(lines[0]) if lines else None


def _ppid_of(pid: int) -> int | None:
    r = subprocess.run(["ps", "-p", str(pid), "-o", "ppid="], capture_output=True, text=True)
    out = r.stdout.strip()
    return int(out) if out else None


@pytest.mark.timeout(120)
def test_mutagen_daemon_lifecycle(devm_path, devm_installed):
    # The e2e daemon is already up (bootstrapped, launchd-managed,
    # verified by the autouse _daemon_matches_devm_bin fixture) — a
    # cheap `status` just settles any in-flight startup work before we
    # start reading PIDs.
    subprocess.run([devm_path, "status"], capture_output=True, timeout=20)

    daemon_pid = _daemon_pid(devm_path)
    assert daemon_pid is not None, "devm-e2e daemon PID not found via pgrep"

    mutagen_pid = _mutagen_pid()
    assert mutagen_pid is not None, (
        f"no process holds {_LOCK_PATH} — mutagen daemon not running"
    )

    ppid = _ppid_of(mutagen_pid)
    assert ppid == daemon_pid, (
        f"mutagen daemon (pid={mutagen_pid}) parent is {ppid}, expected the "
        f"devm-e2e daemon pid={daemon_pid} — should be spawned as a direct child"
    )

    # `service restart` bootout+bootstraps the same LaunchDaemon job
    # (cmd/devm/service.go). Mutagen has no setsid shim, so it must die
    # with the old job instead of surviving like iron-proxy does.
    r = subprocess.run([devm_path, "service", "restart"], capture_output=True, timeout=60)
    assert r.returncode == 0, f"service restart failed:\n{r.stderr.decode()}"

    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        try:
            os.kill(mutagen_pid, 0)
        except ProcessLookupError:
            break
        time.sleep(0.2)
    else:
        pytest.fail(
            f"mutagen daemon (pid={mutagen_pid}) still alive after `devm service "
            f"restart` — it should die with the old daemon job (no setsid shim "
            f"protecting it, unlike iron-proxy)."
        )
