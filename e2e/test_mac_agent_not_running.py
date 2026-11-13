"""Bridge resilience: when Mac has no reachable SSH_AUTH_SOCK, guest
ssh fails cleanly, daemon logs once, no crash.
"""
from __future__ import annotations

import pytest

from helpers import e2e_project, mac_env_unset

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_mac_agent_missing():
    with e2e_project("ssh-no-agent") as p:
        vm_alias = f"devm-{p.vm_name}"
        p.set_devm_yaml({"ssh": [vm_alias]})
        p.start()
        # Clear the Mac user's SSH_AUTH_SOCK for the duration of this call.
        with mac_env_unset("SSH_AUTH_SOCK"):
            res = p.run_in_guest(
                ["ssh", "-o", "ConnectTimeout=5", vm_alias, "true"],
                check=False,
            )
            assert res.returncode != 0, "ssh must fail when agent unreachable"
        # Daemon should still be responsive — a subsequent benign request
        # succeeds. `devm-e2e status` with cwd=project is the cheapest
        # daemon-ping available.
        res = p.mac_cmd(["/usr/local/bin/devm-e2e", "status"])
        assert res.returncode == 0, (
            f"daemon must not crash on SSH_AUTH_SOCK='': "
            f"stdout={res.stdout!r} stderr={res.stderr!r}"
        )
