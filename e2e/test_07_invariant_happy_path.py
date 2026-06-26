"""07: full happy path — VM running, shell works, worker daemon up after reconcile.

A project with a service that has a background startup command is
cold-started via the tart_sandbox fixture (minimal config), then the
workspace is updated with the full service config and devm reconcile
applies the startup service. The interactive shell reaches a prompt
and the background worker process is verified alive inside the VM.

What this pins:
  - Cold-start brings the VM to 'running' state.
  - Interactive shell reaches a prompt.
  - A service `startup:` entry with `background: True` leaves a
    long-lived process running inside the VM, detectable via pgrep.
  - devm stop --yes transitions running -> stopped.

What it doesn't cover (tested elsewhere):
  - install: step at cold-create -> test_17c or later.
  - Live port add via reconcile -> test_08.
  - Env injection (project + service vars) -> test_11.
  - Service add/remove churn -> test_21.
  - Port publishing to host -> sbx-era mechanic, not pinned here.
"""
import time

import pytest

from helpers import Shell

pytestmark = pytest.mark.devm


@pytest.mark.timeout(120)
def test_invariant_happy_path(workspace, devm, tart_sandbox):
    # tart_sandbox fixture already cold-started the VM with minimal config.
    assert tart_sandbox.state() == "running", (
        f"expected VM to be running after cold-start; got {tart_sandbox.state()!r}"
    )

    # Update config to add worker service with background startup.
    workspace.write_devmyaml(
        services={
            "worker": {
                "startup": [
                    {"command": ["sh", "-c", "while true; do sleep 60; done"],
                     "background": True}
                ],
            },
        },
    )

    # Open shell — devm reconcile applies the new service config (LIVE bucket).
    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)

        # Worker daemon is running. Filter the pgrep self-match: the
        # sh process running `pgrep -af MARKER` has MARKER in its own
        # argv and would otherwise return a false positive. `grep -v
        # pgrep` drops that line.
        r = tart_sandbox.exec_shell(
            "pgrep -af 'while true.*sleep 60' 2>/dev/null | grep -v pgrep | grep -q . && echo OK || echo MISS"
        )
        assert r.ok, f"worker check exec failed: {r.stderr}"
        assert r.stdout.strip() == "OK", f"worker daemon not found: {r.stdout.strip()!r}"

        sh.exit(timeout=30)

    # Anchor-alive: explicitly stop after shell exit.
    devm.stop(yes=True)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if tart_sandbox.state() == "stopped":
            return
        time.sleep(0.5)
    pytest.fail("sandbox never reached stopped")
