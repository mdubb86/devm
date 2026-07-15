"""Inspect / kill a project's iron-proxy process by scanning `ps`.

Mirrors the inline `_iron_proxy_pid_for` pattern duplicated in
test_44 and test_74 — pulled out here so heal e2e tests (and the
restart_isolated_daemon seam test) can share it instead of
reimplementing the ps-scan a third and fourth time.
"""
from __future__ import annotations

import os
import signal
import subprocess


def project_proxy_pid(project_id: str) -> int | None:
    """Return the PID of the running iron-proxy for this project, or None.

    Matches on the `-config .../iron-proxy/<project_id>.yaml` argv
    fragment the daemon always spawns iron-proxy with (see
    internal/serviceapi/ironproxy.go SpawnIronProxy). Works in both
    isolated mode (config under a private runtime dir) and install
    mode (config under ~/Library/Application Support/devm/).
    """
    r = subprocess.run(
        ["ps", "-axo", "pid=,command="],
        capture_output=True, text=True, check=True,
    )
    needle = f"/iron-proxy/{project_id}.yaml"
    for line in r.stdout.splitlines():
        if needle in line:
            return int(line.strip().split(None, 1)[0])
    return None


def project_proxy_running(project_id: str) -> bool:
    """True if a live iron-proxy process is found for this project."""
    return project_proxy_pid(project_id) is not None


def kill_project_proxy(project_id: str) -> None:
    """SIGKILL the project's iron-proxy, if one is found. No-op otherwise."""
    pid = project_proxy_pid(project_id)
    if pid is not None:
        try:
            os.kill(pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
