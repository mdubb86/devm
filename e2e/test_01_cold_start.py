"""01: cold-create → install marker → exit → stopped."""
import time

import pytest

from helpers import Shell, sbx

pytestmark = pytest.mark.devm


@pytest.mark.timeout(60)
def test_cold_start(workspace, devm, sandbox_name):
    workspace.write_devmyaml(install=["touch /tmp/install-marker"])

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=60)
        # install ran at create → marker present
        sh.run_check("test -e /tmp/install-marker", expect_zero=True, timeout=30)
        sh.exit(timeout=30)

    # Anchor-alive: shell exit no longer auto-stops the sandbox.
    # Explicitly stop and verify it reaches 'stopped' within ~15s.
    devm.stop(yes=True)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "stopped":
            return
        time.sleep(0.5)
    pytest.fail(f"sandbox {sandbox_name} never reached 'stopped' within 15s")
