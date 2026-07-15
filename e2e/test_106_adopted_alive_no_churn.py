"""106: an adopted iron-proxy that's still alive is left alone — no churn.

Negative-space sibling of test_101: adoption itself must not bounce a
healthy proxy. Cold-start, capture the PID, cross the
`restart_isolated_daemon()` seam WITHOUT killing the proxy, and assert
the PID is unchanged and still running — the daemon's startup
adoption path (DiscoverIronProxies + Supervisor.Adopt) re-attaches to
the live process rather than respawning it.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.proxy import project_proxy_pid, project_proxy_running

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_adopted_alive_no_churn(devm, workspace, sandbox_name, devm_installed, restart_isolated_daemon):
    workspace.write_devmyaml(
        install=["true"],
        services={"sleep": {"exec": ["/bin/sleep", "infinity"], "restart": "always"}},
    )

    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    pid_before = project_proxy_pid(workspace.slug)
    assert pid_before is not None, (
        f"iron-proxy should be running for project {workspace.slug!r} after cold-start"
    )

    restart_isolated_daemon()

    assert project_proxy_running(workspace.slug), (
        "iron-proxy should still be running after daemon restart"
    )
    pid_after = project_proxy_pid(workspace.slug)
    assert pid_after == pid_before, (
        f"adopted-but-alive iron-proxy was churned across daemon restart: "
        f"before={pid_before} after={pid_after} — it should be left alone, not respawned"
    )
