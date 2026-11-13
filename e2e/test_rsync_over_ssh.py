"""rsync -e ssh works transparently over the devm jump.

Pins that the feature's ssh config isn't rsync-hostile (no oddball
options, no agent requirement rsync can't satisfy). Uses the default
ssh transport — same ProxyJump + IdentityAgent path as a plain `ssh`.
"""
from __future__ import annotations

import pytest

from helpers import e2e_project, ssh_add_and_cleanup

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_rsync_over_ssh():
    with e2e_project("ssh-rsync") as p:
        vm_alias = f"devm-{p.vm_name}"
        # rsync isn't in the base guest image; declare it so the guest
        # installs it during provision.
        p.set_devm_yaml({"ssh": [vm_alias], "packages": ["rsync"]})
        p.start()
        with ssh_add_and_cleanup(p.client_key_path):
            res = p.run_in_guest(
                ["rsync", "-e", "ssh",
                 f"{vm_alias}:/etc/hostname", "/tmp/hostname-copy"],
                check=False,
            )
            assert res.returncode == 0, (
                f"rsync failed: stdout={res.stdout!r} stderr={res.stderr!r}"
            )
            out = p.run_in_guest(["cat", "/tmp/hostname-copy"]).stdout.strip()
            assert out == p.vm_name, (
                f"unexpected hostname: want {p.vm_name!r}, got {out!r}"
            )
