"""100: the `devm service restart` seam works.

Task 8's heal e2e tests depend on the daemon-restart seam to reach the
"adopted, dead-proxy-stays-dead" state (a verified spike found the
supervisor does NOT auto-restart an *adopted* proxy, only a
freshly-spawned one). Before those tests can rely on it, this pins
that:

  - `devm service restart` actually kills and relaunches the daemon
    (launchctl bootout+bootstrap of com.devm.e2e.service).
  - The relaunch survives — `devm status --json` reports the daemon
    running afterward.
  - The project's VM is still present/running post-restart.
  - The project's iron-proxy is untouched by the restart — same PID
    before and after, i.e. it was adopted rather than killed or
    respawned (mirrors the install-mode pin in test_44).
"""
from __future__ import annotations

import json
import subprocess
import time

import pytest

from helpers.proxy import project_proxy_pid

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_daemon_restart_seam(devm, workspace, sandbox_name, devm_installed, devm_path):
    # Cold-start: no secrets, no network config needed — just get a VM
    # (and its iron-proxy) up.
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    def _status() -> dict:
        r = subprocess.run(
            [devm.path, "status", "--json"],
            cwd=str(workspace.path), capture_output=True, timeout=20,
        )
        assert r.returncode == 0, f"devm status failed:\n{r.stderr.decode()}"
        return json.loads(r.stdout.decode())

    body_before = _status()
    assert body_before["daemon"]["running"] is True
    assert body_before["project"]["state"] == "running", (
        f"VM should be running after cold-start; project={body_before['project']}"
    )

    pid_before = project_proxy_pid(workspace.slug)
    assert pid_before is not None, (
        f"iron-proxy should be running for project {workspace.slug!r} after cold-start"
    )

    # The seam under test: `devm service restart` (shells out to sudo
    # internally for the launchctl kickstart).
    r = subprocess.run(
        [devm_path, "service", "restart"],
        capture_output=True, timeout=60,
    )
    assert r.returncode == 0, f"service restart failed:\n{r.stderr.decode()}"
    # Short settle for DiscoverIronProxies + Supervisor.Adopt to finish
    # on the new daemon process (mirrors test_44's post-restart settle).
    time.sleep(2)

    # Daemon came back and still knows about this project's VM.
    body_after = _status()
    assert body_after["daemon"]["running"] is True, (
        f"daemon should report running after restart; body={body_after}"
    )
    assert body_after["project"]["state"] == "running", (
        f"VM should still be present/running after daemon restart; "
        f"project={body_after['project']}"
    )

    # Iron-proxy was adopted, not killed/respawned — same PID.
    pid_after = project_proxy_pid(workspace.slug)
    assert pid_after is not None, (
        "iron-proxy should still be running after daemon restart"
    )
    assert pid_after == pid_before, (
        f"iron-proxy PID changed across `devm service restart`: "
        f"before={pid_before} after={pid_after} — adoption didn't happen"
    )
