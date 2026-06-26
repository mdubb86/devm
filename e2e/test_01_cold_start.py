"""01: cold-start a project from scratch -> VM is running -> shell works -> stop.

`devm shell` on a project with no existing sandbox creates one and
opens an interactive shell that reaches a prompt. The tart_sandbox
fixture drives the cold-start via `devm shell -- true`; this test
verifies the post-cold-start state and the stop lifecycle step.

What this pins:
  - Cold-create path brings the VM to 'running' state.
  - Interactive shell reaches a prompt.
  - Shell exit does NOT auto-stop the VM (the explicit stop-and-verify
    at the end is non-redundant).
  - devm stop --yes transitions running -> stopped within ~15s.

What it doesn't cover (tested elsewhere):
  - Install step executed at cold-create -> test_17c or later.
  - Teardown (sandbox removal) -> test_05.
  - Cold-start with templates -> test_19.
  - Cold-start variants (docker base, curl install) -> test_24, test_25.
"""
import time

import pytest

from helpers import Shell

pytestmark = pytest.mark.devm


@pytest.mark.timeout(60)
def test_cold_start(workspace, devm, tart_sandbox):
    # tart_sandbox fixture already cold-started the VM.
    assert tart_sandbox.state() == "running", (
        f"expected VM to be running after cold-start; got {tart_sandbox.state()!r}"
    )

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=60)
        # Basic sanity: shell can execute a command.
        sh.run_check("echo hello-from-shell", expect_zero=True, timeout=15)
        sh.exit(timeout=30)

    # Anchor-alive: sandbox stays running after shell exits.
    assert tart_sandbox.state() == "running", (
        "sandbox should still be running after shell exit (anchor-alive)"
    )

    devm.stop(yes=True)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if tart_sandbox.state() == "stopped":
            return
        time.sleep(0.5)
    pytest.fail("sandbox never reached stopped")
