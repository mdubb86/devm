"""16: non-tty devm reconcile of a LIVE change succeeds without prompt.

LIVE-bucket changes (port add/remove/change, env, network add/remove,
template) don't recreate the sandbox, so devm shouldn't require user
confirmation. A non-tty `devm reconcile` (stdin from /dev/null,
mimicking CI / scripted invocations) on a LIVE change should apply
cleanly: exit 0, sandbox keeps running, change is observable.

What this pins:
  - Non-tty path: `devm reconcile` (no --yes, no tty) exits 0 for a
    LIVE-bucket change.
  - The change is applied (a new port published, observable via
    `sbx ports --json`).
  - The pre-existing user shell survives.
  - Companion to test_09 (which covers non-tty under RECREATE-required
    changes, where the non-tty guard returns exit 2).

What it doesn't cover (tested elsewhere):
  - Non-tty under RECREATE-required changes -> test_09.
  - Live port reconcile under tty/--yes -> test_08.
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers import Shell, sbx, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(90)
def test_reconcile_live_nontty(workspace, devm, sandbox_name):
    # Cold-start with one service (8080); the LIVE change is adding a
    # second service (9090). Port add is in the LIVE bucket so reconcile
    # should NOT require confirmation.
    workspace.write_devmyaml(services={
        "api": {"port": 8080},
    })

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)

        workspace.patch_devmyaml(services={
            "api": {"port": 8080},
            "web": {"port": 9090},
        })

        # Non-tty reconcile: stdin from /dev/null. No --yes flag.
        # Expectation: succeeds without prompt because the change is LIVE.
        p = subprocess.run(
            [devm.path, "reconcile"],
            cwd=str(workspace.path),
            stdin=subprocess.DEVNULL,
            capture_output=True, timeout=60, check=False,
        )
        assert p.returncode == 0, (
            f"expected exit 0 (LIVE change, no prompt needed); got {p.returncode}\n"
            f"stdout: {p.stdout.decode()!r}\nstderr: {p.stderr.decode()!r}"
        )

        # The new port should be published.
        deadline = time.monotonic() + 10
        seen = False
        while time.monotonic() < deadline:
            mappings = sbx.ports(sandbox_name)
            if any(m["sandbox_port"] == 9090 for m in mappings):
                seen = True
                break
            time.sleep(0.25)
        assert seen, "new port 9090 not published after non-tty reconcile"

        # The user shell survives -- LIVE change, no restart.
        sh.run_check("echo still-alive", expect_zero=True, timeout=15)

        sh.exit(timeout=30)

    stop_and_wait_stopped(devm, sandbox_name)
