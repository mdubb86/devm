"""320: a top-level `cron:` entry runs a devm.sh function on a systemd
timer. The timer never runs during the boot-time open window, applies
live on reconcile, and is removed live when the entry is dropped.

Flow:
  1. devm.sh defines tick-a, tick-b and a startup() that records
     whether the tick-a timer is active. devm.yaml schedules tick-a
     every minute. Cold-start.
  2. The timer was `inactive` while startup() ran (timers start only
     after network enforcement) and is not boot-persistent
     (`is-enabled` is `static`).
  3. tick-a fires and appends to its tick file.
  4. Swap the entry to tick-b, approve and reconcile. The reconcile
     reports cron_add/cron_remove as applied live, with no restart
     requested, and the guest's boot id is unchanged.
  5. tick-b fires; the tick-a units and wrapper are gone.
"""
from __future__ import annotations

import json
import subprocess
import time

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


_DEVM_SH = """#!/usr/bin/env bash
set -eo pipefail
tick-a() { date +%s >> /home/devm/tick-a; }
tick-b() { date +%s >> /home/devm/tick-b; }
startup() { systemctl is-active devm-cron-tick-a.timer > /home/devm/timer-at-startup || true; }
"""

_EVERY_MINUTE = "* * * * *"

# A minutely timer fires within 60s of starting; the margin covers the
# wrapper's own run time.
_FIRE_DEADLINE_SECONDS = 150
_POLL_INTERVAL_SECONDS = 5

_BOOT_ID = "/proc/sys/kernel/random/boot_id"


def _diagnostics(sandbox: TartSandbox, fn: str) -> str:
    status = sandbox.exec("systemctl", "status", f"devm-cron-{fn}.timer", "--no-pager")
    journal = sandbox.exec(
        "bash", "-c", f"sudo journalctl -u devm-cron-{fn}.service --no-pager | tail"
    )
    return (
        f"systemctl status devm-cron-{fn}.timer:\n{status.stdout}{status.stderr}\n"
        f"journalctl -u devm-cron-{fn}.service:\n{journal.stdout}{journal.stderr}"
    )


def _wait_for_tick(sandbox: TartSandbox, fn: str) -> None:
    """Waits on real time: a minutely timer has to reach its next minute."""
    path = f"/home/devm/{fn}"
    deadline = time.monotonic() + _FIRE_DEADLINE_SECONDS
    while True:
        r = sandbox.exec(
            "bash", "-c", f"if [ -e {path} ]; then wc -l < {path}; else echo 0; fi"
        )
        if r.exit_code != 0:
            pytest.fail(f"reading {path} failed ({r.exit_code}): {r.stderr}")
        if int(r.stdout.strip()) >= 1:
            return
        if time.monotonic() >= deadline:
            pytest.fail(
                f"{path} still empty {_FIRE_DEADLINE_SECONDS}s after it should "
                f"have fired.\n{_diagnostics(sandbox, fn)}"
            )
        time.sleep(_POLL_INTERVAL_SECONDS)


def _boot_id(sandbox: TartSandbox) -> str:
    r = sandbox.exec("cat", _BOOT_ID)
    assert r.exit_code == 0, f"read boot_id failed: {r.stderr}"
    return r.stdout.strip()


@pytest.mark.slow
@pytest.mark.timeout(1200)
def test_cron_function_fires_applies_live_and_starts_after_enforcement(
    workspace, devm, sandbox_name
):
    workspace.write_devmyaml(no_repo=True, cron={"tick-a": _EVERY_MINUTE})
    workspace.write_devm_sh(_DEVM_SH)

    sandbox = TartSandbox(name=sandbox_name)

    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=600,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    # startup() ran in the open window, before enforcement.
    r = sandbox.exec("cat", "/home/devm/timer-at-startup")
    assert r.exit_code == 0, f"timer-at-startup missing: {r.stderr}"
    assert r.stdout.strip() == "inactive", (
        f"tick-a timer was not inactive during startup(): {r.stdout!r}"
    )

    # Boot-time start comes from the enforced provisioning step, not from
    # an [Install] section.
    r = sandbox.exec("systemctl", "is-enabled", "devm-cron-tick-a.timer")
    assert r.stdout.strip() == "static", (
        f"tick-a timer should be static, not boot-persistent: "
        f"stdout={r.stdout!r} stderr={r.stderr!r}"
    )

    _wait_for_tick(sandbox, "tick-a")

    boot_before = _boot_id(sandbox)

    workspace.write_devmyaml(no_repo=True, cron={"tick-b": _EVERY_MINUTE})
    devm.approve()
    r = devm.reconcile(json_out=True)
    result = json.loads(r.stdout.decode())

    applied = {(c["kind"], c["key"]) for c in result["applied"]}
    assert ("cron_add", "tick-b") in applied, f"cron_add tick-b not applied live: {result}"
    assert ("cron_remove", "tick-a") in applied, f"cron_remove tick-a not applied live: {result}"
    assert "restart_required" not in result and "recreate_required" not in result, (
        f"cron change must not need a restart or recreate: {result}"
    )
    assert _boot_id(sandbox) == boot_before, "guest rebooted during cron reconcile"

    _wait_for_tick(sandbox, "tick-b")

    for path in (
        "/etc/systemd/system/devm-cron-tick-a.timer",
        "/etc/systemd/system/devm-cron-tick-a.service",
        "/opt/devm/cron-wrappers/tick-a.sh",
    ):
        r = sandbox.exec("test", "-e", path)
        assert r.exit_code != 0, f"{path} still present after cron entry removed"

    r = sandbox.exec("systemctl", "list-timers", "--all", "devm-cron-tick-a.timer")
    assert "devm-cron-tick-a.timer" not in r.stdout, (
        f"tick-a timer still listed after removal:\n{r.stdout}"
    )
