"""Start project A, note its allocated ProjectIP; kick the daemon
(`devm service restart`) and verify A is still reachable at the SAME IP
afterwards — proves AdoptIronProxies rehydrates
`projectInfo[...].ProjectIP` from the persisted StateSnapshot on daemon
restart instead of re-allocating (see docs/superpowers/specs/
2026-07-19-per-project-bind-isolation-design.md's "Daemon restart" bullet).

Adapted from the task-7 brief's sketch:
  - `devm service restart` (the same mechanism test_44 already pins for
    iron-proxy adoption) needs cached sudo credentials (Touch ID) to
    kick launchd — prime with `sudo -v` first if running this test
    standalone.
  - `devm status --all --json` carries no `project_ip` field
    (internal/serviceapi/statusall.go's ProjectStatus only has
    name/vm_running/proxy). Reads the daemon's persisted StateSnapshot
    file directly instead — the same on-disk field (json:"project_ip")
    AdoptIronProxies itself reads back on daemon restart.
"""
from __future__ import annotations

import json
import subprocess
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


def _runtime_dir() -> Path:
    return Path.home() / "Library" / "Application Support" / "devm-e2e"


def _project_ip(project_id: str) -> str | None:
    p = _runtime_dir() / "state" / f"{project_id}.json"
    if not p.exists():
        return None
    try:
        return json.loads(p.read_text()).get("project_ip") or None
    except (OSError, ValueError):
        return None


def _restart_daemon(devm_path: str) -> None:
    r = subprocess.run(
        [devm_path, "service", "restart"],
        capture_output=True, timeout=60,
    )
    assert r.returncode == 0, f"service restart failed:\n{r.stderr.decode()!r}"


@pytest.mark.timeout(420)
def test_project_ip_survives_daemon_restart(devm, workspace, devm_path):
    workspace.write_devmyaml()

    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()!r}"

    ip_before = _project_ip(workspace.slug)
    assert ip_before and ip_before.startswith("127.42.0."), (
        f"unexpected ProjectIP before restart: {ip_before!r}"
    )

    _restart_daemon(devm_path)

    ip_after = _project_ip(workspace.slug)
    assert ip_after == ip_before, (
        f"ProjectIP changed across daemon restart: {ip_before!r} -> "
        f"{ip_after!r} — AdoptIronProxies isn't rehydrating projectInfo "
        f"from the persisted StateSnapshot"
    )
