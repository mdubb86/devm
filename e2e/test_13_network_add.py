"""13: adding a network allow rule via reconcile registers the domain LIVE.

A project with no network rules cold-starts; the user edits devm.yaml
to add a single `network.allowed_domains` entry, then runs
`devm reconcile`. Within a short deadline, the added domain shows up
in the global `sbx policy ls` network output, proving devm forwarded
the rule to the sbx network policy. The pre-existing user shell stays
alive across the reconcile (LIVE bucket — network-add does not
recreate the sandbox).

What this pins:
  - A new `allowed_domains` entry is propagated to the sbx global
    network policy after `devm reconcile`.
  - The propagation is observable via `sbx.policy_list_network()`
    within the 10s deadline.
  - Network-add is a LIVE-bucket change: the existing user shell
    survives reconcile (`echo still-alive` runs).
  - The `.invalid` test domain is used so the rule is safe to leak if
    the cleanup fixture fails.

What it doesn't cover (tested elsewhere):
  - Cold-start network policy registration -> sbx contract tests.
  - Sandbox-scoping of the allow list -> test_sbx_contract_11.
  - Removing a network rule via reconcile -> not yet pinned.
"""
import time

import pytest

from helpers import Shell, sbx, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(75)
def test_network_add(workspace, devm, sandbox_name, policy_registrar):
    workspace.write_devmyaml(services={
        "worker": {
            "startup": [
                {"command": ["sh", "-c", "while true; do sleep 60; done"],
                 "background": True}
            ],
        },
    })

    # .invalid TLD can never resolve in the real world; safe to leave
    # behind if a worst-case crash beats the cleanup fixture.
    domain = f"{sandbox_name}.example.invalid"
    policy_registrar(domain)

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)

        # Add a network allow rule.
        workspace.patch_devmyaml(
            services={
                "worker": {
                    "startup": [
                        {"command": ["sh", "-c", "while true; do sleep 60; done"],
                         "background": True}
                    ],
                },
            },
            network={"allowed_domains": [domain]},
        )
        devm.reconcile(yes=True, timeout=60)

        # The domain should now appear in the global sbx network policy.
        deadline = time.monotonic() + 10
        found = False
        while time.monotonic() < deadline:
            if domain in sbx.policy_list_network():
                found = True
                break
            time.sleep(0.25)
        assert found, f"domain {domain!r} not in sbx policy ls after reconcile add"

        # LIVE — user shell should survive.
        sh.run_check("echo still-alive", expect_zero=True, timeout=15)

        sh.exit(timeout=30)

    # Anchor-alive: explicitly stop after shell exit.
    stop_and_wait_stopped(devm, sandbox_name)
