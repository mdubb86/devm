"""21: adding then removing a service via reconcile is LIVE on both edges.

User starts with one service ("api"), then patches devm.yaml to add a
second service ("web"), reconciles, and observes the new host port
published. Then removes "web" from devm.yaml, reconciles, and observes
the host port unpublished. User shell survives both reconciles.

What this pins:
  - Adding a service entry + reconcile publishes its host port live.
  - Removing a service entry + reconcile unpublishes its host port live.
  - The pre-existing service ("api") port stays published across both.
  - The user shell remains alive across both reconciles (LIVE bucket).

What it doesn't cover (tested elsewhere):
  - LIVE port ADD on an existing service (test_08).
  - LIVE port REMOVE/CHANGE on an existing service (test_12).
  - Reconcile prompt+yes flow under recreate (test_09).
"""
from __future__ import annotations
import pytest

from helpers import Shell, sbx, stop_and_wait_stopped

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
    stop_and_wait_stopped(devm, sandbox_name)
