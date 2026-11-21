"""302: a cron: snapshot entry gets a systemd timer installed in-guest;
removing the entry (and restarting the VM so install.sh re-runs)
removes the timer.

Flow:
  1. Declare one snapshot entry `heartbeat` with `cron: "0 3 * * *"`
     and `keep: 7`. devm.sh writes a trivial backup/restore pair so
     validation passes.
  2. Cold-start — install.sh at boot drops the rendered service and
     timer under /etc/systemd/system/ and enables the timer.
  3. Assert both unit files exist, the timer has the expected
     OnCalendar + Persistent=false, `is-enabled` returns `enabled`,
     and `list-timers` shows a next-elapse row.
  4. Drop the heartbeat entry and restart the VM via stop + start;
     install.sh's orphan-cleanup disables and removes the units.
  5. Assert the unit files are gone and list-timers no longer
     mentions it.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


_DEVM_SH = """#!/usr/bin/env bash
set -eo pipefail
backup_heartbeat()  { printf x; }
restore_heartbeat() { cat > /dev/null; }
"""


_SERVICE = "/etc/systemd/system/devm-snapshot-heartbeat.service"
_TIMER = "/etc/systemd/system/devm-snapshot-heartbeat.timer"


@pytest.mark.slow
@pytest.mark.timeout(1200)
def test_cron_timer_installed_then_removed(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        no_repo=True,
        snapshots={
            "heartbeat": {"cron": "0 3 * * *", "keep": 7},
        },
    )
    workspace.write_devm_sh(_DEVM_SH)

    sandbox = TartSandbox(name=sandbox_name)

    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=600,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    # Unit files land in /etc/systemd/system/.
    for path in (_SERVICE, _TIMER):
        r = sandbox.exec("test", "-f", path)
        assert r.exit_code == 0, f"{path} missing after install.sh ran"

    # Timer body pins OnCalendar + Persistent=false.
    r = sandbox.exec("cat", _TIMER)
    assert r.exit_code == 0, f"read timer failed: {r.stderr}"
    body = r.stdout
    assert "OnCalendar=*-*-* 03:00:00" in body, (
        f"timer OnCalendar doesn't match the cron spec:\n{body}"
    )
    assert "Persistent=false" in body, (
        f"timer Persistent should be false, body:\n{body}"
    )

    r = sandbox.exec("systemctl", "is-enabled", "devm-snapshot-heartbeat.timer")
    assert r.exit_code == 0 and r.stdout.strip() == "enabled", (
        f"heartbeat timer not enabled: stdout={r.stdout!r} stderr={r.stderr!r}"
    )

    r = sandbox.exec(
        "systemctl", "list-timers", "--all", "devm-snapshot-heartbeat.timer"
    )
    assert r.exit_code == 0, f"list-timers failed: {r.stderr}"
    assert "devm-snapshot-heartbeat.timer" in r.stdout, (
        f"heartbeat timer missing from list-timers:\n{r.stdout}"
    )

    # Drop the heartbeat entry. write_devmyaml with no `snapshots` key
    # yields a config that has no snapshots at all — the manifest ships
    # empty, and install.sh's orphan-cleanup disables + removes the
    # units at next boot.
    workspace.write_devmyaml(no_repo=True)
    # The hook functions are no longer referenced; keep devm.sh harmless.
    workspace.write_devm_sh("#!/usr/bin/env bash\nset -eo pipefail\n")

    devm.approve()
    devm.stop(timeout=60)
    sandbox.wait_state("stopped", timeout=30)

    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=600,
    )
    assert r.returncode == 0, f"restart after heartbeat removal failed:\n{r.stderr.decode()}"

    for path in (_SERVICE, _TIMER):
        r = sandbox.exec("test", "-f", path)
        assert r.exit_code != 0, f"{path} still present after orphan cleanup"

    r = sandbox.exec(
        "systemctl", "list-timers", "--all", "devm-snapshot-heartbeat.timer"
    )
    # list-timers over a nonexistent unit prints an empty table with a
    # trailing line of counts; it must NOT re-list the heartbeat unit.
    assert "devm-snapshot-heartbeat.timer" not in r.stdout, (
        f"heartbeat timer still listed after removal:\n{r.stdout}"
    )
