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
    pts/N bash processes visible via `sbx exec` into sandbox_name).
  - After both shells exit, the sandbox stays running (anchor-alive)
    until `devm stop --yes` brings it to 'stopped'.

What it doesn't cover (tested elsewhere):
  - Concurrent reconcile from two shells (not yet pinned -- gap candidate).
  - Cold-create + install marker -> test_01.
"""
import subprocess

import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(90)
def test_concurrent_shells_share_sandbox(workspace, devm, sandbox_name):
    workspace.write_devmyaml()

    with Shell(devm, cwd=str(workspace.path)) as first:
        first.expect_prompt(timeout=60)
        with Shell(devm, cwd=str(workspace.path)) as second:
            second.expect_prompt(timeout=60)
            # Both shells alive in the same sandbox: there should be
            # >= 2 bashes on pts/N inside the VM.
            out = subprocess.run(
                ["sbx", "exec", sandbox_name, "bash", "-c",
                 "ps -eo comm,tty | grep -c '^bash *pts/'"],
                capture_output=True, timeout=15, check=True,
            ).stdout.decode().strip()
            assert int(out) >= 2, f"expected >=2 pty bashes; got {out}"
            second.exit(timeout=30)
        first.exit(timeout=30)

    # Anchor-alive: explicitly stop after both shells exit.
    stop_and_wait_stopped(devm, sandbox_name)
