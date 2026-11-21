"""03: devm stop brings a running sandbox to 'stopped' via the interactive prompt.

devm shell on a fresh project cold-creates a sandbox. The user exits
the shell (anchor stays alive). The user then runs `devm stop` and
answers the confirmation prompt. The sandbox reaches 'stopped' and the
attached shell dies.

The --yes path (skip the prompt) is pinned in test_52, whose stop step
already exercises `devm.stop(yes=True)` and checks the running->stopped
transition en route to its restart assertion — no need for a second
boot here just to re-prove --yes skips the prompt.

What this pins:
  - Interactive path: `devm stop` prompts; answering 'y' completes the stop.
  - The user shell dies when the sandbox stops.
  - sandbox transitions from 'running' to 'stopped'.

What it doesn't cover (tested elsewhere):
  - --yes path (skips prompt) -> test_52.
  - Teardown semantics (sandbox REMOVED, not just stopped) -> test_05.
  - Non-tty stop flow -> not yet pinned (gap candidate).
"""
from __future__ import annotations

import re
import time

import pexpect
import pytest

from helpers import Shell

pytestmark = pytest.mark.devm


@pytest.mark.timeout(90)
def test_stop(workspace, devm, tart_sandbox):
    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=60)

        # Spawn `devm stop` in a separate pexpect process to answer y.
        stop = pexpect.spawn(devm.path, ["stop"], cwd=str(workspace.path),
                             encoding="utf-8", timeout=30, dimensions=(40, 200))
        stop.expect(
            re.escape(f"Stop VM {tart_sandbox.name}?") + r".*\[y/N\]:\s*",
            timeout=30,
        )
        stop.sendline("y")
        stop.expect(pexpect.EOF, timeout=30)
        stop.close(force=True)

        # The user shell must die when the sandbox stops.
        sh.expect_eof(timeout=30)

    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if tart_sandbox.state() == "stopped":
            return
        time.sleep(0.5)
    pytest.fail("sandbox never reached stopped")
