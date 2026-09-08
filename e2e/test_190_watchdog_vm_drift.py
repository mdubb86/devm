"""190: state watchdog reconciles /status/all when the VM stops out-of-band.

`tart stop <vm>` bypasses devm's stop path. The state watchdog's VM
check should notice the discrepancy within its tick (60s) and update
the cache; devm status --all --json then reports vm_running=false.
"""
from __future__ import annotations

import json
import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
def test_watchdog_reconciles_vm_after_external_tart_stop(devm, workspace):
    workspace.write_devmyaml()
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=180)
    assert r.returncode == 0, r.stderr.decode()
    try:
        # Sanity: /status/all shows running.
        r = subprocess.run([devm.path, "status", "--all", "--json"],
                           cwd=str(workspace.path), capture_output=True, timeout=30)
        assert r.returncode == 0
        rows = json.loads(r.stdout.decode())
        row = next((x for x in rows if x["name"] == workspace.vm_name), None)
        assert row is not None and row["vm_running"] is True

        # External stop — bypasses devm.
        subprocess.run(["tart", "stop", workspace.vm_name],
                       capture_output=True, timeout=60, check=True)

        # Wait for the watchdog to notice + reconcile. Up to 90s.
        deadline = time.monotonic() + 90
        reconciled = False
        while time.monotonic() < deadline:
            r = subprocess.run([devm.path, "status", "--all", "--json"],
                               cwd=str(workspace.path), capture_output=True, timeout=15)
            if r.returncode == 0:
                rows = json.loads(r.stdout.decode())
                row = next((x for x in rows if x["name"] == workspace.vm_name), None)
                if row is not None and row["vm_running"] is False:
                    reconciled = True
                    break
            time.sleep(2)
        assert reconciled, "watchdog did not reconcile /status/all vm_running=false within 90s"
    finally:
        subprocess.run([devm.path, "teardown", "--yes"],
                       cwd=str(workspace.path), timeout=60, capture_output=True)
