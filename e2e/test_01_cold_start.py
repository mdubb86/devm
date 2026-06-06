"""01: cold-create → install marker → exit → stopped."""
import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(60)
def test_cold_start(workspace, devm, sandbox_name):
    workspace.write_devmyaml(install=["touch /tmp/install-marker"])

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=60)
        # install ran at create → marker present
        sh.run_check("test -e /tmp/install-marker", expect_zero=True, timeout=30)
        sh.exit(timeout=30)

    stop_and_wait_stopped(devm, sandbox_name)
