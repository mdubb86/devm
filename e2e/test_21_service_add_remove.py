"""21: adding a new service to devm.yaml + reconcile = port published live.
    Then removing it + reconcile = port unpublished live. Shell survives both.
"""
from __future__ import annotations
import time
import pytest

from helpers import Shell, sbx

pytestmark = pytest.mark.devm


@pytest.mark.timeout(90)
def test_service_add_remove(workspace, devm, sandbox_name):
    # Start with a single service "api" on canonical 8080.
    workspace.write_devmyaml(services={"api": {"port": 8080}})
    api_host = workspace.port_offset + 8080
    web_host = workspace.port_offset + 3000

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)

        # Initial port published, additional one not yet.
        sbx.wait_for_port_published(sandbox_name, host_port=api_host, timeout=15)
        sbx.wait_for_port_absent(sandbox_name, host_port=web_host, timeout=5)

        # Add a NEW service "web" with canonical 3000.
        workspace.patch_devmyaml(services={
            "api": {"port": 8080},
            "web": {"port": 3000},
        })
        devm.reconcile(yes=True, timeout=60)

        # Both ports now published.
        sbx.wait_for_port_published(sandbox_name, host_port=api_host, timeout=15)
        sbx.wait_for_port_published(sandbox_name, host_port=web_host, timeout=15)

        # User shell survived the live change.
        sh.run_check("echo still-alive", expect_zero=True, timeout=15)

        # Remove "web" entirely.
        workspace.patch_devmyaml(services={
            "api": {"port": 8080},
        })
        devm.reconcile(yes=True, timeout=60)

        # api still up, web gone.
        sbx.wait_for_port_published(sandbox_name, host_port=api_host, timeout=15)
        sbx.wait_for_port_absent(sandbox_name, host_port=web_host, timeout=15)

        sh.run_check("echo still-alive-2", expect_zero=True, timeout=15)

        sh.exit(timeout=30)

    # Anchor-alive: explicitly stop after shell exit.
    devm.stop(yes=True)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "stopped":
            return
        time.sleep(0.5)
    pytest.fail(f"sandbox {sandbox_name} never reached 'stopped'")
