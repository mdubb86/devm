"""104: a regular command (`devm shell`) warns on iron-proxy drift and
proceeds — it does not heal and does not fail.

Sibling of test_103 for the daemonHandshake warning path shared by
`shell`/`stop`/`teardown` (cmd/devm/handshake.go: daemonHandshake
prints to stderr and returns nil — reporting only).

Sequence:
  1. Cold-start, cross the adoption seam, kill the proxy (stays dead).
  2. `devm shell -- true` against the already-running VM: exits 0
     (proceeds despite drift), stderr carries the drift warning
     (mentions iron-proxy + reconcile), and the proxy is still dead
     afterward (shell did not heal it).
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers.proxy import kill_project_proxy, project_proxy_running

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_command_warns_and_proceeds(devm, workspace, sandbox_name, devm_installed, restart_isolated_daemon):
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

    # shell must not have healed it as a side effect.
    assert not project_proxy_running(workspace.slug), (
        "iron-proxy is running after `devm shell`; commands must only "
        "warn on drift, never heal it"
    )
