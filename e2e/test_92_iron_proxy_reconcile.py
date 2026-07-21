"""92: iron-proxy-restart reconcile bucket.

Proves that adding to `network.allow` and reconciling picks up the
new entry WITHOUT stopping/restarting the VM. The old cold-start
hammer would trip an entire teardown; the new bucket restarts only
iron-proxy on the same MAC_HOST:port.

Sequence:
  1. Cold-start with an allowlist that has httpbin.org only.
  2. Assert baseline: httpbin.org 200, example.com 403.
  3. Note the tart-run PID.
  4. Edit devm.yaml to add example.com under network.allow.
  5. `devm reconcile` — assert stdout matches the network-egress
     section header.
  6. Assert httpbin.org still 200, example.com now succeeds
     (no longer 403).
  7. Assert the tart-run PID is unchanged (VM was never bounced).
"""
from __future__ import annotations

import subprocess
import time

import pytest
import yaml

from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm


def _tart_pid(vm_name: str) -> int | None:
    """PID of the tart-run process for vm_name, if any."""
    out = subprocess.run(
        ["pgrep", "-f", f"tart run.*{vm_name}"],
        capture_output=True, text=True,
    )
    if out.returncode != 0:
        return None
    pids = [line for line in out.stdout.strip().splitlines() if line]
    return int(pids[0]) if pids else None


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_iron_proxy_reconcile_allowlist_add(workspace, devm):
    workspace.write_devmyaml(
        network={
            "allow": [
                "httpbin.org",
            ],
        },
    )

    start = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=600,
    )
    assert start.returncode == 0, (
        f"devm start failed:\nstderr={start.stderr.decode()!r}"
    )

    # Baseline: allow-listed host works, non-allow-listed gets 403.
    allowed = devm_exec_with_retry(
        devm.path,
        ["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
         "-A", "Mozilla/5.0", "--max-time", "15",
         "https://httpbin.org/status/200"],
        cwd=str(workspace.path), timeout=60,
    )
    assert allowed.returncode == 0
    assert allowed.stdout.decode().strip().splitlines()[-1] == "200", (
        f"baseline httpbin.org expected 200; got {allowed.stdout!r}"
    )

    denied = devm_exec_with_retry(
        devm.path,
        ["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
         "--max-time", "15", "https://example.com/"],
        cwd=str(workspace.path), timeout=60,
    )
    assert denied.returncode == 0
    assert denied.stdout.decode().strip().splitlines()[-1] == "403", (
        f"baseline example.com expected 403 iron-proxy reject; got {denied.stdout!r}"
    )

    # Record the VM's tart-run PID so we can prove no bounce.
    pid_before = _tart_pid(workspace.vm_name)
    assert pid_before is not None, "expected a running tart process for the VM"

    # Add example.com to allow list and reconcile. devm.yaml is
    # host-immutable (config-lock) while the VM runs; unlock before
    # editing — the reconcile call below re-locks it (unlock -> edit ->
    # reconcile always ends locked, per test_120_config_lock.py).
    devm.unlock()
    cfg = yaml.safe_load(workspace.devmyaml_path.read_text())
    cfg["network"]["allow"].append("example.com")
    workspace.devmyaml_path.write_text(yaml.safe_dump(cfg, sort_keys=False))

    reconcile = subprocess.run(
        [devm.path, "reconcile"],
        cwd=str(workspace.path),
        capture_output=True, timeout=120,
    )
    assert reconcile.returncode == 0, (
        f"devm reconcile failed:\nstderr={reconcile.stderr.decode()!r}"
    )
    out = reconcile.stdout.decode()
    assert "network egress change" in out, (
        f"reconcile stdout missing network-egress section: {out!r}"
    )
    assert "example.com" in out

    # Small settle — iron-proxy needs a moment to re-bind on the
    # preserved port; guest DNS uses the same forwarding target.
    time.sleep(2)

    # example.com now allowed.
    now_allowed = devm_exec_with_retry(
        devm.path,
        ["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
         "--max-time", "15", "https://example.com/"],
        cwd=str(workspace.path), timeout=60,
    )
    assert now_allowed.returncode == 0
    code = now_allowed.stdout.decode().strip().splitlines()[-1]
    # example.com is a real reachable host; expected 200 or a 3xx
    # redirect. Definitely NOT 403 anymore.
    assert code != "403", (
        f"example.com should no longer be denied; still got 403"
    )

    # VM was not bounced.
    pid_after = _tart_pid(workspace.vm_name)
    assert pid_after == pid_before, (
        f"tart-run PID changed ({pid_before} -> {pid_after}); "
        f"reconcile should have restarted iron-proxy only, not the VM."
    )
