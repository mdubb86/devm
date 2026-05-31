"""13: network add via reconcile — domain registered with sbx policy, LIVE."""
import time

import pytest

from helpers import Shell, sbx


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

    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "stopped":
            return
        time.sleep(0.5)
    pytest.fail(f"sandbox {sandbox_name} never reached 'stopped'")
