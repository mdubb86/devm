"""03: devm stop brings a running sandbox to 'stopped' under both prompt and --yes paths.

devm shell on a fresh project cold-creates a sandbox. The user exits
the shell (anchor stays alive). The user then runs `devm stop` — with
or without `--yes`. Either way the sandbox reaches 'stopped'; the
prompt path requires answering 'y'.

What this pins:
  - Interactive path: `devm stop` prompts; answering 'y' completes the stop.
  - --yes path: skips prompt entirely; same end state.
  - sandbox transitions from 'running' to 'stopped' in both cases.

What it doesn't cover (tested elsewhere):
  - Teardown semantics (sandbox REMOVED, not just stopped) → test_05.
  - Non-tty stop flow → not yet pinned (gap candidate).
"""
from __future__ import annotations

import re
import time

import pexpect
import pytest

from helpers import Shell, sbx

pytestmark = pytest.mark.devm


@pytest.mark.timeout(90)
@pytest.mark.parametrize("mode", ["prompt", "yes"], ids=["prompt", "yes"])
def test_stop(workspace, devm, sandbox_name, mode):
    workspace.write_devmyaml()

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=60)

        if mode == "prompt":
            # Spawn `devm stop` in a separate pexpect process to answer y.
            stop = pexpect.spawn(devm.path, ["stop"], cwd=str(workspace.path),
                                 encoding="utf-8", timeout=30, dimensions=(40, 200))
            stop.expect(re.escape(f"Stop sandbox {sandbox_name}?") + r".*\[y/N\]:\s*", timeout=30)
            stop.sendline("y")
            stop.expect(pexpect.EOF, timeout=30)
            stop.close(force=True)
        else:
            devm.stop(yes=True, timeout=30)

        # The user shell must die when the sandbox stops.
        sh.expect_eof(timeout=30)

    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "stopped":
            return
        time.sleep(0.5)
    pytest.fail("sandbox never reached stopped")
