"""12: LIVE port remove + change via reconcile; no shell restart."""
import time

import pytest

from helpers import Shell, sbx


@pytest.mark.timeout(90)
def test_ports_remove_change(workspace, devm, sandbox_name):
    workspace.write_devmyaml(services={
        "api": {"port": 8080},
        "web": {"port": 3000},
        "worker": {
            "startup": [
                {"command": ["sh", "-c", "while true; do sleep 60; done"],
                 "background": True}
            ],
        },
    })

    api_old_host = workspace.port_offset + 8080
    web_host = workspace.port_offset + 3000
    api_new_host = workspace.port_offset + 8081

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)

        # Both ports published initially.
        sbx.wait_for_port_published(sandbox_name, host_port=api_old_host, timeout=15)
        sbx.wait_for_port_published(sandbox_name, host_port=web_host, timeout=15)

        # Remove `web`, change api 8080 -> 8081.
        workspace.patch_devmyaml(services={
            "api": {"port": 8081},
            "worker": {
                "startup": [
                    {"command": ["sh", "-c", "while true; do sleep 60; done"],
                     "background": True}
                ],
            },
        })
        devm.reconcile(yes=True, timeout=60)

        # New api port present; old api + web absent.
        sbx.wait_for_port_published(sandbox_name, host_port=api_new_host, timeout=15)
        sbx.wait_for_port_absent(sandbox_name, host_port=web_host, timeout=15)
        sbx.wait_for_port_absent(sandbox_name, host_port=api_old_host, timeout=15)

        # User shell still alive (LIVE port changes don't restart).
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
