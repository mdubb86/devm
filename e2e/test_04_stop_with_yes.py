"""04: devm stop --yes skips prompt."""
import time

import pytest

from helpers import Shell, sbx


@pytest.mark.timeout(90)
def test_stop_with_yes(workspace, devm, sandbox_name):
    workspace.write_devmyaml()

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=60)
        devm.stop(yes=True, timeout=30)
        sh.expect_eof(timeout=30)

    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "stopped":
            return
        time.sleep(0.5)
    pytest.fail("sandbox never reached stopped")
