"""188: state watchdog respawns iron-proxy after an out-of-band kill.

Pins the state-watchdog's ability to detect drift (iron-proxy process
killed by SIGKILL out-of-band) and repair it (respawn), reflecting the
final state in the cache-backed /status/all response within the
watchdog's tick.
"""
from __future__ import annotations

import json
import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


def _iron_proxy_pid(project_name: str) -> int | None:
    r = subprocess.run(["pgrep", "-f", f"iron-proxy-{project_name}"],
                       capture_output=True, timeout=10)
    if r.returncode != 0:
        return None
    pid = r.stdout.decode().strip().split("\n")[0]
    return int(pid) if pid else None


@pytest.mark.timeout(600)
def test_watchdog_respawns_iron_proxy_after_external_kill(devm, workspace):
    workspace.write_devmyaml()
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=180)
    assert r.returncode == 0, r.stderr.decode()
    try:
        pid = _iron_proxy_pid(workspace.vm_name)
        assert pid is not None, "iron-proxy should be running after devm start"

        subprocess.run(["kill", "-9", str(pid)], check=True)
        time.sleep(2)
        assert _iron_proxy_pid(workspace.vm_name) is None

        deadline = time.monotonic() + 90
        respawned_pid = None
        while time.monotonic() < deadline:
            new_pid = _iron_proxy_pid(workspace.vm_name)
            if new_pid is not None and new_pid != pid:
                respawned_pid = new_pid
                break
            time.sleep(2)
        assert respawned_pid is not None, "watchdog did not respawn iron-proxy within 90s"

        r = subprocess.run([devm.path, "status", "--all", "--json"],
                           cwd=str(workspace.path), capture_output=True, timeout=30)
        assert r.returncode == 0
        rows = json.loads(r.stdout.decode())
        row = next((x for x in rows if x["name"] == workspace.vm_name), None)
        assert row is not None
        assert row["proxy"]["status"] == "ok"
    finally:
        subprocess.run([devm.path, "teardown", "--yes"],
                       cwd=str(workspace.path), timeout=60, capture_output=True)
