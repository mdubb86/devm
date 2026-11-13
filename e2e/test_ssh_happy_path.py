"""ssh: feature — happy path through a real VM.

Guest SSHes to a per-test fixture sshd on Mac loopback via the
project's jump. The fixture pre-primes Mac's ssh_config + known_hosts
for the alias, so CheckKnownHosts passes at cold-start precondition
time (the self-loopback `devm-<vm>` cold-start chicken-and-egg is
side-stepped).
"""
from __future__ import annotations

import pytest

from helpers import e2e_project, local_sshd_target

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_ssh_happy_path():
    with e2e_project("ssh-happy") as p, local_sshd_target() as alias:
        p.set_devm_yaml({"ssh": [alias]})
        p.start()
        out = p.run_in_guest(
            ["ssh", alias, "echo", "hello-from-ssh-target"],
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
