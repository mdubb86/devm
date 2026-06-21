"""09: devm reconcile prompt-flow under recreate-required changes.

When a devm.yaml change would force a sandbox recreate, devm prompts
the user before tearing down. This pins two answers to that prompt:
  - non-tty: stdin isn't a tty -> devm exits 2 without recreating
  - yes:     --yes flag bypasses prompt -> sandbox is removed (caller
             can then re-shell to trigger the recreate)

What this pins:
  - non-tty path: exit code 2 with next_action=needs_approval, sandbox
    state unchanged (user shell still alive).
  - --yes path: sandbox is removed (user shell dies).

What it doesn't cover (tested elsewhere):
  - The actual recreate flow -> test_14 (install-change forces recreate),
    test_36 (startup-change forces recreate).
"""
from __future__ import annotations

import json
import subprocess

import pytest

from helpers import Shell, sbx, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(120)
@pytest.mark.parametrize("mode", ["non_tty", "yes"], ids=["non_tty", "yes"])
def test_reconcile_prompt_flow(workspace, devm, sandbox_name, mode):
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

        # Edit the startup command -> KindStartupChange is TEARDOWN+SHELL
        # because sbx caches the kit at create-time and re-runs the
        # cached startup on restart. New startup commands need sbx rm +
        # recreate to take effect. Pinned by test_36.
        workspace.patch_devmyaml(services={
            "worker": {
                "startup": [
                    {"command": ["sh", "-c", "while true; do sleep 90; done"],
                     "background": True}
                ],
            },
        })

        if mode == "non_tty":
            # Run reconcile --json with stdin from /dev/null (non-TTY).
            # Expect exit 2 and JSON with next_action=needs_approval. _run()
            # inherits the test process's stdin, which may be a TTY; we must
            # explicitly detach to exercise the non-TTY guard.
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
            stop_and_wait_stopped(devm, sandbox_name)
        else:  # mode == "yes"
            # reconcile --yes performs the recreate. devm may exit non-zero in
            # some recreate flows (sandbox vanishes underneath); we assert on
            # the resulting state, not on this call's exit code.
            devm.reconcile(yes=True, timeout=60, check=False)

            # User shell dies because the sandbox is removed under it.
            sh.expect_eof(timeout=30)

            # reconcile does NOT relaunch a shell -- sandbox should be
            # GONE (TEARDOWN bucket = sbx rm, not sbx stop).
            assert not sbx.sandbox_exists(sandbox_name), (
                f"sandbox {sandbox_name} still exists after reconcile "
                f"--yes on a startup change (should have been removed)"
            )
