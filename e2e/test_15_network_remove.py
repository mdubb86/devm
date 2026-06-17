"""15: removing a network allow rule via reconcile drops the domain LIVE.

A project cold-starts with one `network.allowed_domains` entry; the
user edits devm.yaml to remove it, then runs `devm reconcile`. Within
a short deadline, the domain disappears from the sbx network policy,
proving devm forwarded the removal. The pre-existing user shell stays
alive across the reconcile (LIVE bucket — network-remove mirrors
network-add).

What this pins:
  - An existing `allowed_domains` entry is dropped from the sbx
    network policy after `devm reconcile` removes it from devm.yaml.
  - The drop is observable via `sbx.policy_list_network()` within
    the 10s deadline.
  - Network-remove is a LIVE-bucket change: the existing user shell
    survives reconcile (`echo still-alive` runs).
  - The `.invalid` test domain is used so the rule is safe to leak
    if the cleanup fixture fails.

What it doesn't cover (tested elsewhere):
  - Adding a network rule via reconcile -> test_13.
  - Sandbox-scoping of the allow list -> test_sbx_contract_11.
"""
import time

import pytest

from helpers import Shell, sbx, stop_and_wait_stopped

pytestmark = pytest.mark.devm

WORKER_STARTUP = {
    "worker": {
        "startup": [
            {"command": ["sh", "-c", "while true; do sleep 60; done"],
             "background": True}
        ],
    },
}


@pytest.mark.timeout(90)
def test_network_remove(workspace, devm, sandbox_name, policy_registrar):
    domain = f"{sandbox_name}.example.invalid"
    policy_registrar(domain)

    workspace.write_devmyaml(
        services=WORKER_STARTUP,
        network={"allowed_domains": [domain]},
    )

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)

        # Sanity: cold-start should have registered the domain via the
        # runtime sbx policy allow (not as a kit-baked rule).
        assert domain in sbx.policy_list_network(), (
            f"setup precondition: {domain!r} not in sbx policy after cold-start"
        )

        workspace.patch_devmyaml(
            services=WORKER_STARTUP,
            network={"allowed_domains": []},
        )
        devm.reconcile(yes=True, timeout=60)

        deadline = time.monotonic() + 10
        gone = False
        while time.monotonic() < deadline:
            if domain not in sbx.policy_list_network():
                gone = True
                break
            time.sleep(0.25)
        assert gone, f"domain {domain!r} still in sbx policy ls after reconcile remove"

        # LIVE — user shell should survive.
        sh.run_check("echo still-alive", expect_zero=True, timeout=15)

        sh.exit(timeout=30)

    stop_and_wait_stopped(devm, sandbox_name)
