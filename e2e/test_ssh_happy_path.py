"""ssh: feature — happy path through a real VM.

Guest SSHes to itself via the Mac loopback, reusing the inbound-SSH
spec's existing `Host devm-<vm-name>` alias as the target. No new Mac
ssh config mutation — the alias already exists.

Setup only needs the per-project client key in Mac's ssh-agent so the
IdentityAgent path has something to serve.
"""
from __future__ import annotations

import pytest

from helpers import e2e_project, ssh_add_and_cleanup

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_ssh_happy_path():
    with e2e_project("ssh-happy") as p:
        vm_alias = f"devm-{p.vm_name}"
        # Project declares the inbound-SSH alias as its ssh: list entry.
        p.set_devm_yaml({"ssh": [vm_alias]})
        p.start()
        # Load the per-project client key into Mac's ssh-agent for the
        # duration of this test.
        with ssh_add_and_cleanup(p.client_key_path):
            out = p.run_in_guest(
                ["ssh", vm_alias, "echo", "hello-from-ssh-target"],
            )
            assert "hello-from-ssh-target" in out.stdout, (
                f"ssh output missing marker: stdout={out.stdout!r} "
                f"stderr={out.stderr!r}"
            )
        # Global Constraint: guest holds exactly one key — the per-
        # project devm-jump-id. Reject any other private-key file in
        # the guest's ~/.ssh.
        listing = p.run_in_guest(["ls", "/home/devm/.ssh"]).stdout.split()
        suspicious = [
            f for f in listing
            if f.endswith((".pem", "id_rsa", "id_ed25519"))
        ]
        assert not suspicious, (
            f"guest ~/.ssh/ holds unknown private keys: {suspicious} "
            f"(full listing: {listing})"
        )
