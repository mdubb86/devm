"""Start project A, note its allocated ProjectIP; kick the daemon
(kill + relaunch) and verify A is still reachable at the SAME IP
afterwards — proves AdoptIronProxies rehydrates
`projectInfo[...].ProjectIP` from the persisted StateSnapshot on daemon
restart instead of re-allocating (see docs/superpowers/specs/
2026-07-19-per-project-bind-isolation-design.md's "Daemon restart" bullet).

Adapted from the task-7 brief's sketch:
  - The brief's draft used a raw `pgrep -f "devm serve"` + `sudo kill
    -TERM`. That only maps onto a REAL, launchd-managed daemon
    (E2E_ISOLATE=0 / after `devm install`). Under the DEFAULT isolated
    e2e lane (E2E_ISOLATE=1 — what `just e2e-one` runs unless
    overridden), there's no launchd entry to restart and no root
    process to kill; conftest.py's `restart_isolated_daemon` fixture is
    the existing, already-proven seam for exactly this case (kills +
    relaunches the foreground `devm serve --foreground` against the
    same DEVM_RUNTIME_DIR, no sudo needed — see its use pattern
    described alongside test_73/test_100). This test branches on
    $E2E_ISOLATE so it exercises the real adoption path in BOTH lanes:
    the isolated kill/respawn seam when isolated, or `devm service
    restart` (the same mechanism test_44 already pins for iron-proxy
    adoption) when not.
  - `devm status --all --json` carries no `project_ip` field
    (internal/serviceapi/statusall.go's ProjectStatus only has
    name/vm_running/proxy). Reads the daemon's persisted StateSnapshot
    file directly instead — the same on-disk field (json:"project_ip")
    AdoptIronProxies itself reads back on daemon restart.

MANUAL/SUDO NOTE: the non-isolated branch (E2E_ISOLATE=0, i.e. running
against a real `devm install`'d daemon) calls `devm service restart`,
which needs cached sudo credentials (Touch ID) to kick launchd — prime
with `sudo -v` first if running this test outside the default isolated
lane. The default isolated lane (`just e2e-one
test_per_project_ip_adopt`) needs no sudo at all.
"""
from __future__ import annotations

import json
import os
import subprocess
from pathlib import Path

import pytest

from helpers.portbinder import helper_installed

pytestmark = [
    pytest.mark.devm,
    pytest.mark.skipif(
        not helper_installed(),
        reason="requires devm install (portbinder helper); run `devm install` on this machine to enable",
    ),
]


def _runtime_dir() -> Path:
    if os.environ.get("E2E_ISOLATE") == "1":
        d = os.environ.get("DEVM_RUNTIME_DIR")
        if d:
            return Path(d)
    return Path.home() / "Library" / "Application Support" / "devm"


def _project_ip(project_id: str) -> str | None:
    p = _runtime_dir() / "state" / f"{project_id}.json"
    if not p.exists():
        return None
    try:
        return json.loads(p.read_text()).get("project_ip") or None
    except (OSError, ValueError):
        return None


def _restart_daemon(devm_path: str, request) -> None:
    """Kick the daemon and wait for it to come back, using whichever
    mechanism matches how this run's daemon was started."""
    if os.environ.get("E2E_ISOLATE") == "1":
        restart = request.getfixturevalue("restart_isolated_daemon")
        restart()
    else:
        r = subprocess.run(
            [devm_path, "service", "restart"],
            capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"service restart failed:\n{r.stderr.decode()!r}"


@pytest.mark.timeout(420)
def test_project_ip_survives_daemon_restart(devm, workspace, devm_path, request):
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

    _restart_daemon(devm_path, request)

    ip_after = _project_ip(workspace.slug)
    assert ip_after == ip_before, (
        f"ProjectIP changed across daemon restart: {ip_before!r} -> "
        f"{ip_after!r} — AdoptIronProxies isn't rehydrating projectInfo "
        f"from the persisted StateSnapshot"
    )
