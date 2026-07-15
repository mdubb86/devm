"""103: `devm status` REPORTS a missing iron-proxy — it does NOT heal it.

The key "report, don't heal" proof for the collapsed self-heal design:
only `devm reconcile` mutates. `devm status` is read-only even when it
detects drift.

Sequence:
  1. Cold-start, cross the adoption seam, kill the proxy (same setup
     as test_101 — confirmed to stay dead).
  2. `devm status` in the project: stdout reports
     `iron-proxy: MISSING (run 'devm reconcile')`, exit code is
     ExitReconcileRequired (4).
  3. The proxy is STILL dead afterward — status must not have
     respawned it as a side effect of probing.
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers.proxy import kill_project_proxy, project_proxy_running

pytestmark = pytest.mark.devm

_EXIT_RECONCILE_REQUIRED = 4


@pytest.mark.timeout(300)
def test_status_reports_missing_does_not_heal(devm, workspace, sandbox_name, devm_installed, restart_isolated_daemon):
    workspace.write_devmyaml(
        install=["true"],
        services={"sleep": {"exec": ["/bin/sleep", "infinity"], "restart": "always"}},
    )

    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"
    assert project_proxy_running(workspace.slug)

    restart_isolated_daemon()
    assert project_proxy_running(workspace.slug), (
        "iron-proxy should survive the daemon restart (adopted)"
    )

    kill_project_proxy(workspace.slug)
    assert not project_proxy_running(workspace.slug)
    time.sleep(3)
    assert not project_proxy_running(workspace.slug), (
        "iron-proxy respawned on its own; self-heal must be reconcile-only"
    )

    status = subprocess.run(
        [devm.path, "status"],
        cwd=str(workspace.path), capture_output=True, timeout=20,
    )
    stdout = status.stdout.decode()
    assert "iron-proxy: MISSING" in stdout, (
        f"expected 'iron-proxy: MISSING' in status output; got stdout={stdout!r} "
        f"stderr={status.stderr.decode()!r}"
    )
    assert status.returncode == _EXIT_RECONCILE_REQUIRED, (
        f"expected exit code {_EXIT_RECONCILE_REQUIRED} (ExitReconcileRequired); "
        f"got {status.returncode}; stdout={stdout!r}"
    )

    # The critical assertion: status must NOT have healed as a
    # side effect of reporting.
    assert not project_proxy_running(workspace.slug), (
        "iron-proxy is running after `devm status` — status must only "
        "report drift, never heal it"
    )
