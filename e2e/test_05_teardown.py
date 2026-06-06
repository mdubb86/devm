"""05: devm teardown REMOVES the sandbox entirely under both prompt and --yes paths.

devm shell cold-creates a sandbox. The user exits the shell. The user
then runs `devm teardown` — with or without `--yes`. Either way the
sandbox is REMOVED (not just stopped); a subsequent `sbx ls` no longer
shows it. The prompt path requires answering 'y'.

What this pins:
  - Interactive path: `devm teardown` prompts; answering 'y' completes removal.
  - --yes path: skips prompt entirely; same end state.
  - sandbox transitions from existing to absent (sbx.sandbox_exists returns False).

What it doesn't cover (tested elsewhere):
  - Stop semantics (sandbox to 'stopped' state, NOT removed) -> test_03.
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
def test_teardown(workspace, devm, sandbox_name, mode):
    workspace.write_devmyaml()

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=60)

        if mode == "prompt":
            # Spawn `devm teardown` in a separate pexpect process to answer y.
            td = pexpect.spawn(devm.path, ["teardown"], cwd=str(workspace.path),
                               encoding="utf-8", timeout=30, dimensions=(40, 200))
            td.expect(re.escape(f"Tear down sandbox {sandbox_name}?") + r".*\[y/N\]:\s*", timeout=30)
            td.sendline("y")
            td.expect(pexpect.EOF, timeout=30)
            td.close(force=True)
        else:
            devm.teardown(yes=True, timeout=30)

        # User shell dies when the sandbox goes away.
        sh.expect_eof(timeout=30)

    # Teardown removes the sandbox (not just stops it).
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if not sbx.sandbox_exists(sandbox_name):
            return
        time.sleep(0.5)
    pytest.fail(f"sandbox {sandbox_name} still exists after teardown")
