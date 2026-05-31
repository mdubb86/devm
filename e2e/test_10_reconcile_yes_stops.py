"""10: reconcile --yes on a recreate-required change stops the sandbox."""
import time

import pytest

from helpers import Shell, sbx


@pytest.mark.timeout(75)
def test_reconcile_yes_stops(workspace, devm, sandbox_name):
    workspace.write_devmyaml(services={
        "worker": {
            "startup": [
                {"command": ["sh", "-c", "while true; do sleep 60; done"],
                 "background": True}
            ],
        },
    })

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)

        # Edit the startup command: startup change is STOP+SHELL (recreate).
        workspace.patch_devmyaml(services={
            "worker": {
                "startup": [
                    {"command": ["sh", "-c", "while true; do sleep 90; done"],
                     "background": True}
                ],
            },
        })

        # reconcile --yes performs the recreate. devm may exit non-zero in
        # some recreate flows (sandbox vanishes underneath); we assert on
        # the resulting state, not on this call's exit code.
        devm.reconcile(yes=True, timeout=60, check=False)

        # User shell dies because the sandbox was stopped under it.
        sh.expect_eof(timeout=30)

    # reconcile does NOT relaunch a shell — sandbox should be stopped.
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "stopped":
            return
        time.sleep(0.5)
    pytest.fail(f"sandbox {sandbox_name} never reached 'stopped'")
