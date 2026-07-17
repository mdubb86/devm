"""05: devm teardown REMOVES the sandbox entirely via the interactive prompt.

devm shell cold-creates a sandbox. The user exits the shell. The user
then runs `devm teardown` and answers the confirmation prompt. The
sandbox is REMOVED (not just stopped); tart list no longer shows it.

The --yes path (skip the prompt) is pinned by test_53, which proves
the pure `--yes` teardown -> absent transition without an attached
shell muddying the signal — no need for a second boot here just to
re-prove --yes skips the prompt.

What this pins:
  - Interactive path: `devm teardown` prompts; answering 'y' completes removal.
  - User shell dies when the sandbox goes away.
  - sandbox transitions from existing to absent (tart_sandbox.state() == "absent").

What it doesn't cover (tested elsewhere):
  - --yes path (skips prompt) -> test_53.
  - Stop semantics (sandbox to 'stopped' state, NOT removed) -> test_03.
"""
from __future__ import annotations

import re
import time

import pexpect
import pytest

from helpers import Shell

pytestmark = pytest.mark.devm


@pytest.mark.timeout(90)
def test_teardown(workspace, devm, tart_sandbox):
    workspace.write_devmyaml()

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=60)

        # Spawn `devm teardown` in a separate pexpect process to answer y.
        td = pexpect.spawn(devm.path, ["teardown"], cwd=str(workspace.path),
                           encoding="utf-8", timeout=30, dimensions=(40, 200))
        td.expect(
            re.escape(f"Tear down VM {tart_sandbox.name}?") + r".*\[y/N\]:\s*",
            timeout=30,
        )
        td.sendline("y")
        td.expect(pexpect.EOF, timeout=30)
        td.close(force=True)

        # User shell dies when the sandbox goes away.
        sh.expect_eof(timeout=30)

    # Teardown removes the sandbox (not just stops it).
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if tart_sandbox.state() == "absent":
            return
        time.sleep(0.5)
    pytest.fail(f"sandbox {tart_sandbox.name} still exists after teardown")
