"""Pin: an allowlist edit applied via `devm reconcile` does NOT respawn
iron-proxy — the daemon's PolicyAuthority.Set takes effect on the next
request. iron-proxy PID stays stable; guest can reach a newly-allowed
host immediately.

Regression fence for Bug 2 — the ~2s iron-proxy downtime + denial-counter
wipe that KindNetworkAdd/Remove used to trigger is gone.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


def _iron_proxy_pid(project: str) -> int:
    """Return the iron-proxy PID for the given project, or 0 if none."""
    r = subprocess.run(
        ["pgrep", "-f", f"iron-proxy.*{project}"],
        capture_output=True, text=True, timeout=5,
    )
    if r.returncode != 0:
        return 0
    lines = [l.strip() for l in r.stdout.splitlines() if l.strip()]
    return int(lines[0]) if lines else 0


@pytest.mark.timeout(300)
def test_allowlist_edit_leaves_iron_proxy_alive(devm, workspace):
    workspace.write_devmyaml(
        no_repo=True,
        network={"allow": ["example.com"]},
        packages=["curl"],
    )

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # Baseline: iron-proxy is up under some PID.
        pid_before = _iron_proxy_pid(workspace.vm_name)
        assert pid_before > 0, "iron-proxy not running after cold-start"

        # Baseline: httpbin.org is NOT allowlisted; guest curl gets iron-proxy's 403.
        r = subprocess.run(
            [devm.path, "shell", "--",
             "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
             "--max-time", "10", "https://httpbin.org/get"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0 and r.stdout.decode().strip() == "403", (
            f"baseline: expected 403, got rc={r.returncode} body={r.stdout!r}"
        )

        # Add httpbin.org to allow and reconcile.
        workspace.write_devmyaml(
            no_repo=True,
            network={"allow": ["example.com", "httpbin.org"]},
            packages=["curl"],
        )
        r = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"reconcile failed:\n{r.stderr.decode()}"

        # Iron-proxy PID MUST be unchanged — the whole point.
        pid_after = _iron_proxy_pid(workspace.vm_name)
        assert pid_after == pid_before, (
            f"iron-proxy respawned across allowlist edit: "
            f"before={pid_before} after={pid_after}"
        )

        # And the guest can now reach httpbin.org.
        r = subprocess.run(
            [devm.path, "shell", "--",
             "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
             "--max-time", "10", "https://httpbin.org/get"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0 and r.stdout.decode().strip() == "200", (
            f"post-edit: expected 200, got rc={r.returncode} body={r.stdout!r}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
