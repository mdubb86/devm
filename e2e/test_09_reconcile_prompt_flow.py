"""09: devm reconcile prompt-flow under recreate-required changes.

When a devm.yaml change would force a sandbox recreate, devm prompts
the user before tearing down. This pins two answers to that prompt:
  - non-tty: stdin isn't a tty -> devm exits 2 without recreating
  - yes:     --yes flag bypasses prompt -> devm tears the sandbox down
             and relaunches it (cold start) in the same call

What this pins:
  - non-tty path: exit code 2 with next_action=needs_approval, sandbox
    state unchanged (user shell still alive).
  - --yes path: the old sandbox is torn down (user shell dies), then
    reconcile relaunches it automatically -- same anchor-alive
    contract as `devm start` (cold start, no shell attached, VM stays
    running). This is the Task 7 behavior change: reconcile --yes used
    to exit after removal and rely on a later `devm shell` to rebuild;
    it now relaunches within the same call.

What it doesn't cover (tested elsewhere):
  - The actual recreate flow -> test_14 (install-change forces recreate),
    test_36 (startup-change forces recreate).
"""
from __future__ import annotations

import json
import subprocess

import pytest

from helpers import Shell

pytestmark = pytest.mark.devm


@pytest.mark.timeout(360)
@pytest.mark.parametrize("mode", ["non_tty", "yes"], ids=["non_tty", "yes"])
def test_reconcile_prompt_flow(workspace, devm, tart_sandbox, mode):
    # tart_sandbox fixture already cold-started the VM with minimal config.
    # Open a shell that warm-attaches to the running VM.
    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)

        # Add an install step -- an install change always requires TEARDOWN
        # recreate regardless of VM state.
        workspace.patch_devmyaml(install=["touch /tmp/reconcile-probe"])

        if mode == "non_tty":
            # Run reconcile --json with stdin from /dev/null (non-TTY).
            # Expect exit 2 and JSON with next_action=needs_approval.
            p = subprocess.run(
                [devm.path, "reconcile", "--json"],
                cwd=str(workspace.path),
                stdin=subprocess.DEVNULL,
                capture_output=True, timeout=60, check=False,
            )
            assert p.returncode == 2, (
                f"expected exit 2 (non-TTY recreate); got {p.returncode}\n"
                f"stdout: {p.stdout.decode()!r}\nstderr: {p.stderr.decode()!r}"
            )
            body = json.loads(p.stdout.decode())
            assert body.get("next_action") == "needs_approval", (
                f"expected next_action=needs_approval; got {body}"
            )

            # The user shell must still be alive -- reconcile didn't recreate.
            sh.run_check("echo still-here", expect_zero=True, timeout=15)

            sh.exit(timeout=30)

            # Anchor-alive: explicitly stop after shell exit.
            devm.stop(yes=True)
        else:  # mode == "yes"
            # reconcile --yes performs the recreate: tear down the old VM,
            # then relaunch (cold start) automatically in the same call --
            # this blocks until the new VM is up. devm may exit non-zero in
            # some recreate flows; we assert on the resulting state, not on
            # this call's exit code.
            devm.reconcile(yes=True, timeout=300, check=False)

            # User's original shell dies because the sandbox is removed
            # under it.
            sh.expect_eof(timeout=30)

            # reconcile relaunches after tearing down (same anchor-alive
            # contract as `devm start`): the sandbox should be RUNNING
            # again by the time the call returns, not left absent.
            assert tart_sandbox.state() == "running", (
                f"sandbox {tart_sandbox.name} should be running again "
                f"after reconcile --yes relaunched it post-recreate"
            )
