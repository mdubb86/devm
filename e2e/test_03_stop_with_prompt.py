"""03: devm stop with interactive prompt; user answers y."""
import re
import time

import pexpect
import pytest

from helpers import Shell, sbx


@pytest.mark.timeout(90)
def test_stop_with_prompt(workspace, devm, sandbox_name):
    workspace.write_devmyaml()

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=60)

        # Spawn `devm stop` in a separate pexpect process to answer y.
        stop = pexpect.spawn(devm.path, ["stop"], cwd=str(workspace.path),
                             encoding="utf-8", timeout=30, dimensions=(40, 200))
        stop.expect(re.escape(f"Stop sandbox {sandbox_name}?") + r".*\[y/N\]:\s*", timeout=30)
        stop.sendline("y")
        stop.expect(pexpect.EOF, timeout=30)
        stop.close(force=True)

        # The user shell must die when the sandbox stops.
        sh.expect_eof(timeout=30)

    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "stopped":
            return
        time.sleep(0.5)
    pytest.fail("sandbox never reached stopped")
