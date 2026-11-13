"""PINNED DESIGNED BEHAVIOR: a process with socket access CAN request
signatures over arbitrary bytes. This is the signing-oracle risk and
is the irreducible cost of exposing Mac's agent at all — same as
ssh -A. See the design doc's Review Focus.

If this test ever fails because someone "hardened" the bridge by
filtering SIGN_REQUEST bytes, revert the hardening — it will break
real SSH logins silently. Different mitigation needed instead (e.g.
ssh-add -h destination constraints, user-side).
"""
from __future__ import annotations

import pytest

from helpers import e2e_project, ssh_add_and_cleanup

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_signing_oracle_pinned():
    with e2e_project("ssh-signing-oracle") as p:
        vm_alias = f"devm-{p.vm_name}"
        p.set_devm_yaml({"ssh": [vm_alias]})
        p.start()
        with ssh_add_and_cleanup(p.client_key_path):
            # ssh-keygen -Y sign asks for a signature over arbitrary
            # bytes. This MUST succeed — the bridge is designed to
            # forward SIGN_REQUEST untouched. See the comment at the
            # top of the file.
            p.run_in_guest(
                ["sh", "-c",
                 "echo arbitrary > /tmp/data && "
                 "SSH_AUTH_SOCK=/run/devm/ssh-agent.sock "
                 "ssh-keygen -Y sign -f ~/.ssh/devm-jump-id "
                 "-n test /tmp/data"],
            )
            res = p.run_in_guest(
                ["test", "-f", "/tmp/data.sig"],
                check=False,
            )
            assert res.returncode == 0, (
                "signing oracle broken — see pin comment at top of file"
            )
