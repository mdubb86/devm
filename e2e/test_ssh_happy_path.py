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
        # Verify guest ~/.ssh/ holds only devm-managed files — no stray
        # key material the Global Constraints forbid.
        listing = p.run_in_guest(["ls", "/home/devm/.ssh"]).stdout.split()
        assert set(listing) <= {
            "config", "known_hosts", "devm-jump-id", "authorized_keys",
        }, f"unexpected files in guest ~/.ssh: {listing}"
