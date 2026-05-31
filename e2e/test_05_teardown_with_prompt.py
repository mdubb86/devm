"""05: devm teardown with interactive prompt; sandbox is GONE after."""
import re
import time

import pexpect
import pytest

from helpers import Shell, sbx


@pytest.mark.timeout(90)
def test_teardown_with_prompt(workspace, devm, sandbox_name):
    workspace.write_devmyaml()

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=60)

        td = pexpect.spawn(devm.path, ["teardown"], cwd=str(workspace.path),
                           encoding="utf-8", timeout=30, dimensions=(40, 200))
        td.expect(re.escape(f"Tear down sandbox {sandbox_name}?") + r".*\[y/N\]:\s*", timeout=30)
        td.sendline("y")
        td.expect(pexpect.EOF, timeout=30)
        td.close(force=True)

        # User shell dies when the sandbox goes away.
        sh.expect_eof(timeout=30)

    # Teardown removes the sandbox (not just stops it).
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if not sbx.sandbox_exists(sandbox_name):
            return
        time.sleep(0.5)
    pytest.fail(f"sandbox {sandbox_name} still exists after teardown")
