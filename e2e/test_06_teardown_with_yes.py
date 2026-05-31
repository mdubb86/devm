"""06: devm teardown --yes skips prompt; sandbox is GONE after."""
import time

import pytest

from helpers import Shell, sbx


@pytest.mark.timeout(90)
def test_teardown_with_yes(workspace, devm, sandbox_name):
    workspace.write_devmyaml()

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=60)
        devm.teardown(yes=True, timeout=30)
        sh.expect_eof(timeout=30)

    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if not sbx.sandbox_exists(sandbox_name):
            return
        time.sleep(0.5)
    pytest.fail(f"sandbox {sandbox_name} still exists after teardown --yes")
