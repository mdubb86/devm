"""08: live port add via devm reconcile — no shell restart.

A project starts with a single service `api:8080` and a live shell
attached. The user edits devm.yaml to add a second service
`worker:9090` and runs `devm reconcile --yes`. The new canonical port
becomes published to the host without recreating the sandbox, and the
existing shell stays alive across the reconcile.

What this pins:
  - Cold-start already publishes the initial canonical port
    (8080 -> port_offset + 8080).
  - `devm reconcile --yes` on a port-only addition publishes the new
    canonical port (9090 -> port_offset + 9090) live.
  - The user's interactive shell survives the reconcile (a post-
    reconcile `echo` still returns exit 0 in the same shell).

What it doesn't cover (tested elsewhere):
  - Reconcile prompt-flow under recreate -> test_09.
  - Install-change forcing recreate -> test_14.
  - Service add/remove churn -> test_21.
"""
import pytest

from helpers import Shell, sbx, stop_and_wait_stopped

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
    stop_and_wait_stopped(devm, sandbox_name)
