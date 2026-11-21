"""agent bridge — forward REQUEST_IDENTITIES, reject ADD_IDENTITY.

The bridge's whitelist is the design's core mitigation: a guest with
socket access can enumerate and request signatures (the pinned
signing-oracle risk — see test_signing_oracle_pinned.py), but cannot
plant a fresh identity in the Mac's agent. ssh-add <keyfile> issues
ADD_IDENTITY and must bounce.
"""
from __future__ import annotations

import pytest

from helpers import e2e_project, local_sshd_target

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_agent_bridge_filters():
    with e2e_project("ssh-agent-filter") as p, local_sshd_target() as alias:
        p.set_devm_yaml({"ssh": [alias]})
        p.start()
        # ssh-add -L issues REQUEST_IDENTITIES — must succeed and return
        # at least one identity (the fixture loaded its client key into
        # the Mac agent on enter; the test keeps the project's jump
        # client key off the agent on purpose).
        res = p.run_in_guest(
            ["ssh-add", "-L"],
            env={"SSH_AUTH_SOCK": "/run/devm/ssh-agent.sock"},
        )
        assert res.stdout.strip(), (
            f"ssh-add -L must return identities; got empty. "
            f"stderr={res.stderr!r}"
        )
        # ssh-add <keyfile> issues ADD_IDENTITY — must fail.
        p.run_in_guest(
            ["sh", "-c",
             "ssh-keygen -t ed25519 -N '' -f /tmp/plant -C plant"],
        )
        res = p.run_in_guest(
            ["ssh-add", "/tmp/plant"],
            env={"SSH_AUTH_SOCK": "/run/devm/ssh-agent.sock"},
            check=False,
        )
        assert res.returncode != 0, (
            f"ADD_IDENTITY must be rejected: "
            f"stdout={res.stdout!r} stderr={res.stderr!r}"
        )
