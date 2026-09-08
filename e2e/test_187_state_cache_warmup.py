"""187: cache is warm before HTTP serving begins.

The spec's readiness contract: warmup runs synchronously BEFORE the
run.Group starts, so the first call to /status/all after `devm start`
returns fully-populated rows. A regression here would show as an
initially-empty status list right after start.
"""
from __future__ import annotations

import json
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_status_all_populated_immediately_after_service_restart(devm, workspace):
    workspace.write_devmyaml()
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=180)
    assert r.returncode == 0, r.stderr.decode()
    try:
        # Restart the daemon to force a fresh warmup pass with the
        # project already provisioned.
        r = subprocess.run([devm.path, "service", "restart"],
                           capture_output=True, timeout=60)
        assert r.returncode == 0, r.stderr.decode()

        # Immediately query /status/all via `devm status --all --json`.
        # If warmup is broken (async or missing), the row would be
        # absent or have empty fields.
        r = subprocess.run([devm.path, "status", "--all", "--json"],
                           cwd=str(workspace.path), capture_output=True, timeout=15)
        assert r.returncode == 0, r.stderr.decode()
        rows = json.loads(r.stdout.decode())
        row = next((x for x in rows if x["name"] == workspace.vm_name), None)
        assert row is not None, (
            f"warmup should have populated project {workspace.vm_name}; got rows={rows}"
        )
        # vm_running should be True (project was started before restart).
        assert row["vm_running"] is True, row
        # proxy status is a real health string (populated by warmup's
        # iron-proxy check).
        assert row["proxy"]["status"] in ("ok", "missing", "stale"), row["proxy"]
    finally:
        subprocess.run([devm.path, "teardown", "--yes"],
                       cwd=str(workspace.path), timeout=60, capture_output=True)
