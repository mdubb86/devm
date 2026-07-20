"""103: "report, don't heal" contract for a killed iron-proxy, across
all three read-only surfaces — `status`, `shell`, `status --all`.

The key "report, don't heal" proof for the collapsed self-heal design:
only `devm reconcile` mutates. Everything else that notices drift
(status, shell, status --all) only reports/warns.

Merges what were test_103 (status), test_104 (shell), test_105
(status --all) — they shared ~90% identical setup/kill choreography
(cold-start, cross the adoption seam, kill, confirm stays dead). Doing
that ONCE and then running all three read-only surfaces in sequence
against the same still-dead proxy saves two VM boots while keeping all
three distinct assertions intact.

Sequence:
  1. Cold-start, cross the adoption seam (`devm service restart`),
     kill the proxy, confirm it stays dead.
  2. `devm status`: stdout reports `iron-proxy: MISSING (run 'devm
     reconcile')`, exit code is ExitReconcileRequired (4). Proxy still
     dead afterward.
  3. `devm shell -- true` (sibling of the daemonHandshake warning path
     shared by `shell`/`stop`/`teardown`): exits 0 (proceeds despite
     drift), stderr carries the drift warning (mentions iron-proxy +
     reconcile). Proxy still dead afterward.
  4. `devm status --all`, run from a directory with no devm.yaml (it
     ignores cwd): the project's row shows MISSING + required, exit
     code ExitReconcileRequired (4). Proxy still dead afterward.
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers.proxy import kill_project_proxy, project_proxy_running

pytestmark = pytest.mark.devm

_EXIT_RECONCILE_REQUIRED = 4


@pytest.mark.timeout(300)
def test_report_missing_proxy_no_heal(devm, workspace, sandbox_name, devm_installed, devm_path, tmp_path):
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

    # Cross the adoption seam: a freshly-spawned proxy WOULD be
    # auto-restarted by the supervisor if killed; an adopted one won't.
    r = subprocess.run(
        [devm_path, "service", "restart"],
        capture_output=True, timeout=60,
    )
    assert r.returncode == 0, f"service restart failed:\n{r.stderr.decode()}"
    time.sleep(2)
    assert project_proxy_running(workspace.slug), (
        "iron-proxy should survive the daemon restart (adopted)"
    )

    kill_project_proxy(workspace.slug)
    assert not project_proxy_running(workspace.slug), (
        "iron-proxy should be dead immediately after SIGKILL"
    )
    # It must STAY dead — nothing outside `devm reconcile` heals it.
    time.sleep(3)
    assert not project_proxy_running(workspace.slug), (
        "iron-proxy respawned on its own; self-heal must be reconcile-only"
    )

    # ---- (103) `devm status`: reports, doesn't heal. ----
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
    assert not project_proxy_running(workspace.slug), (
        "iron-proxy is running after `devm status` — status must only "
        "report drift, never heal it"
    )

    # ---- (104) `devm shell -- true`: warns and proceeds, doesn't heal. ----
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=60,
    )
    stderr = r.stderr.decode()
    assert r.returncode == 0, (
        f"`devm shell -- true` should proceed (exit 0) despite iron-proxy "
        f"drift; got exit {r.returncode}\nstdout={r.stdout.decode()!r}\nstderr={stderr!r}"
    )
    assert "iron-proxy" in stderr and "reconcile" in stderr, (
        f"expected a drift warning mentioning iron-proxy + reconcile on "
        f"stderr; got stderr={stderr!r}"
    )
    assert not project_proxy_running(workspace.slug), (
        "iron-proxy is running after `devm shell`; commands must only "
        "warn on drift, never heal it"
    )

    # ---- (105) `devm status --all`: reports in the cross-project table,
    # ---- doesn't heal. Run from a directory with no devm.yaml at all —
    # ---- --all ignores cwd.
    status_all = subprocess.run(
        [devm.path, "status", "--all"],
        cwd=str(tmp_path), capture_output=True, timeout=20,
    )
    stdout_all = status_all.stdout.decode()
    assert workspace.slug in stdout_all, (
        f"expected project {workspace.slug!r} in the --all table; got {stdout_all!r}"
    )
    assert "MISSING" in stdout_all, (
        f"expected MISSING in the --all table; got stdout={stdout_all!r}"
    )
    # The project's row specifically: PROJECT / VM / IRON-PROXY / RECONCILE
    # columns — assert the row itself carries both markers, not just
    # that they appear somewhere in the table.
    row = next((line for line in stdout_all.splitlines() if line.startswith(workspace.slug)), None)
    assert row is not None, f"no row found for {workspace.slug!r} in:\n{stdout_all}"
    assert "MISSING" in row and "required" in row, (
        f"expected the project's row to show MISSING + required; got row={row!r}"
    )
    assert status_all.returncode == _EXIT_RECONCILE_REQUIRED, (
        f"expected exit code {_EXIT_RECONCILE_REQUIRED} (ExitReconcileRequired); "
        f"got {status_all.returncode}; stdout={stdout_all!r}"
    )
    assert not project_proxy_running(workspace.slug), (
        "iron-proxy is running after `devm status --all`; it must only "
        "report drift, never heal it"
    )
