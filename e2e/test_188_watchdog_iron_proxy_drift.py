"""188: state watchdog respawns iron-proxy after an out-of-band kill.

Pins the state-watchdog's ability to detect drift (iron-proxy process
killed by SIGKILL out-of-band) and repair it (respawn), reflecting the
final state in the cache-backed /status/all response within the
watchdog's tick.
"""
from __future__ import annotations

import json
import os
import signal
import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


def _iron_proxy_pid(project_name: str) -> int | None:
    # iron-proxy's argv shows `bin/iron-proxy -config <RuntimeDir>/iron-proxy/<project>.yaml`.
    # The <project>.yaml suffix is the only per-project discriminator on the process line.
    r = subprocess.run(["pgrep", "-f", f"iron-proxy.*{project_name}\\.yaml"],
                       capture_output=True, timeout=10)
    if r.returncode != 0:
        return None
    # pgrep may return two pids: the setsid-shim parent and its iron-proxy grandchild.
    # The grandchild is what the watchdog respawns; take the last (highest) pid, which
    # is the leaf iron-proxy — ps prints in pid-ascending order and the grandchild is
    # always spawned after the shim.
    lines = [ln for ln in r.stdout.decode().split("\n") if ln.strip()]
    return int(lines[-1]) if lines else None


@pytest.mark.timeout(600)
def test_watchdog_respawns_iron_proxy_after_external_kill(devm, workspace):
    workspace.write_devmyaml()
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=180)
    assert r.returncode == 0, r.stderr.decode()
    try:
        pid = _iron_proxy_pid(workspace.vm_name)
        assert pid is not None, "iron-proxy should be running after devm start"

        # Kill the leaf iron-proxy. Whether the supervisor's own crash
        # handler respawns it immediately, or the watchdog's 60s tick
        # notices later, the invariant this test pins is the same: the
        # cache ends up reflecting proxy.status=ok.
        os.kill(pid, signal.SIGKILL)

        # Poll `devm status --all --json` for this project's proxy to
        # flip back to "ok". This is the invariant that matters — the
        # cache correctly reflects the respawn. Ignore the CLI's exit
        # code (it returns 4 = reconcile-required when ANY project on
        # the host has proxy.status=missing, which can include
        # unrelated leftovers from other tests).
        deadline = time.monotonic() + 90
        respawned_status = None
        while time.monotonic() < deadline:
            r = subprocess.run([devm.path, "status", "--all", "--json"],
                               cwd=str(workspace.path), capture_output=True, timeout=30)
            if r.stdout:
                try:
                    rows = json.loads(r.stdout.decode())
                except json.JSONDecodeError:
                    rows = []
                row = next((x for x in rows if x["name"] == workspace.vm_name), None)
                if row is not None and row.get("proxy", {}).get("status") == "ok":
                    respawned_status = "ok"
                    break
            time.sleep(2)
        assert respawned_status == "ok", (
            f"watchdog did not restore proxy.status=ok within 90s "
            f"(last stdout={r.stdout.decode()!r}, stderr={r.stderr.decode()!r})"
        )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"],
                       cwd=str(workspace.path), timeout=60, capture_output=True)
