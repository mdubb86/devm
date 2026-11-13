"""agent bridge — forward REQUEST_IDENTITIES, reject ADD_IDENTITY.

The bridge's whitelist is the design's core mitigation: a guest with
socket access can enumerate and request signatures (the pinned
signing-oracle risk — see test_signing_oracle_pinned.py), but cannot
plant a fresh identity in the Mac's agent. ssh-add <keyfile> issues
ADD_IDENTITY and must bounce.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers import e2e_project, ssh_add_and_cleanup

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_agent_bridge_filters():
    with e2e_project("ssh-agent-filter") as p:
        vm_alias = f"devm-{p.vm_name}"
        p.set_devm_yaml({"ssh": [vm_alias]})
        p.start()
        # Derive the exact pubkey line the Mac's agent will return for
        # this project — asserting its presence is stronger than just
        # matching any ed25519 key in the user's Mac agent (which may
        # hold unrelated identities).
        project_pub = subprocess.run(
            ["ssh-keygen", "-y", "-f", str(p.client_key_path)],
            check=True, capture_output=True, text=True,
        ).stdout.strip()
        with ssh_add_and_cleanup(p.client_key_path):
            # ssh-add -L issues REQUEST_IDENTITIES — must succeed and
            # include THIS project's pubkey in the output.
            res = p.run_in_guest(
                ["ssh-add", "-L"],
                env={"SSH_AUTH_SOCK": "/run/devm/ssh-agent.sock"},
            )
            assert project_pub in res.stdout, (
                f"ssh-add -L must include this project's pubkey "
                f"{project_pub!r}; stdout={res.stdout!r} "
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
