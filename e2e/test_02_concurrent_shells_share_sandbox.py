"""02: two concurrent devm shells attach to the same running sandbox.

The user opens a `devm shell`, then opens a second `devm shell` in
the same project from another terminal while the first is still
alive. Both invocations should land inside the SAME sandbox -- the
second must warm-attach rather than cold-create a parallel one.

What this pins:
  - Second `devm shell` on a project with an already-running sandbox
    warm-attaches instead of spinning up a separate sandbox.
  - Both shells reach a prompt concurrently.
  - Both pty bashes live inside one sandbox simultaneously (>=2
    pts/N bash processes visible via tart exec into the VM).
  - After both shells exit, the sandbox stays running (anchor-alive)
    until `devm stop --yes` brings it to 'stopped'.

What it doesn't cover (tested elsewhere):
  - Concurrent reconcile from two shells (not yet pinned -- gap candidate).
  - Cold-create + install marker -> test_01.
"""
import time

import pytest

from helpers import Shell

pytestmark = pytest.mark.devm


@pytest.mark.timeout(90)
def test_concurrent_shells_share_sandbox(workspace, devm, tart_sandbox):
    workspace.write_devmyaml()

    with Shell(devm, cwd=str(workspace.path)) as first:
        first.expect_prompt(timeout=60)
        with Shell(devm, cwd=str(workspace.path)) as second:
            second.expect_prompt(timeout=60)
            # Both shells alive in the same sandbox: there should be
            # >= 2 bashes on pts/N inside the VM.
            r = tart_sandbox.exec_shell(
                "ps -eo comm,tty | grep -c '^bash *pts/'"
            )
            assert r.ok, f"ps exec failed: {r.stderr}"
            assert int(r.stdout.strip()) >= 2, (
                f"expected >=2 pty bashes; got {r.stdout.strip()!r}"
            )
            second.exit(timeout=30)
        first.exit(timeout=30)

    # Anchor-alive: explicitly stop after both shells exit.
    devm.stop(yes=True)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if tart_sandbox.state() == "stopped":
            return
        time.sleep(0.5)
    pytest.fail("sandbox never reached stopped")
