"""12: LIVE port remove + port change via reconcile leave user shell intact.

A project starts with two published services (`api:8080`, `web:3000`)
plus a background `worker`. devm.yaml is then edited to drop `web`
entirely and remap `api` from 8080 to 8081. A single `devm reconcile`
applies both changes against the running sandbox; both the dropped
port and the old api port disappear from the published set, the new
api port appears, and the user shell that was open before the edit is
still alive afterward.

What this pins:
  - Initial cold-start publishes both declared service ports on the
    host (`api` at offset+8080, `web` at offset+3000).
  - A single reconcile that both removes a service and changes another
    service's port succeeds without recreate (LIVE bucket).
  - After reconcile: new api port is published, old api port is gone,
    `web` port is gone.
  - The pre-existing user shell survives the port reconcile (no anchor
    restart, `echo still-alive` runs).

What it doesn't cover (tested elsewhere):
  - Live port ADD via reconcile -> test_08.
  - Reconcile prompt+yes UX under recreate -> test_09.
  - Service add/remove in isolation -> test_21.
"""
import pytest

from helpers import Shell, sbx, stop_and_wait_stopped

pytestmark = pytest.mark.devm


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
    stop_and_wait_stopped(devm, sandbox_name)
