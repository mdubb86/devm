"""rsync -e ssh works transparently over the devm jump.

Pins that the feature's ssh config isn't rsync-hostile (no oddball
options, no agent requirement rsync can't satisfy). Uses the default
ssh transport — same ProxyJump + IdentityAgent path as a plain `ssh`.
"""
from __future__ import annotations

import pytest
import tempfile
from pathlib import Path

from helpers import e2e_project, local_sshd_target

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_rsync_over_ssh():
    # The fixture sshd runs the current user's login shell and inherits
    # its PATH, so rsync on the Mac side is already on PATH. The guest
    # needs rsync installed via packages.
    with tempfile.TemporaryDirectory(prefix="devm-e2e-rsync-") as sink, \
            e2e_project("ssh-rsync") as p, \
            local_sshd_target() as alias:
        dest = Path(sink) / "hostname-copy"
        p.set_devm_yaml({
            "ssh": [alias],
            "packages": ["rsync"],
            "network": {"allow": ["deb.debian.org", "security.debian.org"]},
        })
        p.start()
        # Capture the guest's actual /etc/hostname — in a Tart Debian
        # base image this is "debian", not the VM name, and devm does
        # not override it. The test proves rsync-over-ssh delivers
        # byte-identical content; the content itself is incidental.
        want = p.run_in_guest(["cat", "/etc/hostname"]).stdout.strip()
        assert want, "guest /etc/hostname empty — rsync source would be meaningless"
        res = p.run_in_guest(
            ["rsync", "-e", "ssh", "/etc/hostname",
             f"{alias}:{dest}"],
            check=False,
        )
        assert res.returncode == 0, (
            f"rsync failed: stdout={res.stdout!r} stderr={res.stderr!r}"
        )
        assert dest.exists(), f"rsync target {dest} missing after transfer"
        got = dest.read_text().strip()
        assert got == want, (
            f"rsync delivered wrong content: want {want!r}, got {got!r}"
        )
