"""08: live port add via devm reconcile — no shell restart."""
import time

import pytest

from helpers import Shell, sbx

pytestmark = pytest.mark.devm


@pytest.mark.timeout(90)
def test_reconcile_live_port(workspace, devm, sandbox_name):
    workspace.write_devmyaml(services={"api": {"port": 8080}})

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=60)

        # Cold-start reconcile already published the canonical port.
        sbx.wait_for_port_published(
            sandbox_name, sandbox_port=8080,
            host_port=workspace.port_offset + 8080, timeout=15,
        )

        # Live-add a second service: api + worker.
        workspace.patch_devmyaml(services={
            "api": {"port": 8080},
            "worker": {"port": 9090},
        })
        devm.reconcile(yes=True, timeout=60)

        # New port now published.
        sbx.wait_for_port_published(
            sandbox_name, sandbox_port=9090,
            host_port=workspace.port_offset + 9090, timeout=15,
        )

        # Live change must not restart the user shell.
        sh.run_check("echo still-alive", expect_zero=True, timeout=15)

        sh.exit(timeout=30)

    # Anchor-alive: explicitly stop after shell exit.
    devm.stop(yes=True)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "stopped":
            return
        time.sleep(0.5)
    pytest.fail(f"sandbox {sandbox_name} never reached 'stopped'")
