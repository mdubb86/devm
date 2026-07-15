"""105: `devm status --all` reports a project's missing iron-proxy in
its cross-project table and exits ExitReconcileRequired (4).

Sibling of test_103 for the --all path (FormatStatusAllText / the
RECONCILE column), run from an arbitrary cwd rather than inside the
project (per its own docs: "Works from any directory; ignores
cwd/project").
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers.proxy import kill_project_proxy, project_proxy_running

pytestmark = pytest.mark.devm

_EXIT_RECONCILE_REQUIRED = 4


@pytest.mark.timeout(300)
def test_status_all_reports_missing(devm, workspace, sandbox_name, devm_installed, restart_isolated_daemon, tmp_path):
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

    # Run from a directory with no devm.yaml at all — --all ignores cwd.
    status = subprocess.run(
        [devm.path, "status", "--all"],
        cwd=str(tmp_path), capture_output=True, timeout=20,
    )
    stdout = status.stdout.decode()
    assert workspace.slug in stdout, (
        f"expected project {workspace.slug!r} in the --all table; got {stdout!r}"
    )
    assert "MISSING" in stdout, (
        f"expected MISSING in the --all table; got stdout={stdout!r}"
    )
    # The project's row specifically: PROJECT / VM / IRON-PROXY / RECONCILE
    # columns — assert the row itself carries both markers, not just
    # that they appear somewhere in the table.
    row = next((line for line in stdout.splitlines() if line.startswith(workspace.slug)), None)
    assert row is not None, f"no row found for {workspace.slug!r} in:\n{stdout}"
    assert "MISSING" in row and "required" in row, (
        f"expected the project's row to show MISSING + required; got row={row!r}"
    )
    assert status.returncode == _EXIT_RECONCILE_REQUIRED, (
        f"expected exit code {_EXIT_RECONCILE_REQUIRED} (ExitReconcileRequired); "
        f"got {status.returncode}; stdout={stdout!r}"
    )

    assert not project_proxy_running(workspace.slug), (
        "iron-proxy is running after `devm status --all`; it must only "
        "report drift, never heal it"
    )
