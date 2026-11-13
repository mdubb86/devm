"""Project without ssh: list → feature inert.

No agent-bridge service, no guest ~/.ssh/config, no jump listeners.
Pins the "opt-in via project.ssh" contract: a project that doesn't
ask for the feature doesn't pay any of its costs.
"""
from __future__ import annotations

import pytest

from helpers import e2e_project

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_feature_inert():
    with e2e_project("ssh-inert") as p:
        # No ssh: key in devm.yaml — the helper's default write omits it.
        p.start()
        # Guest-side agent-bridge must NOT be enabled.
        res = p.run_in_guest(
            ["systemctl", "is-enabled", "devm-ssh-agent-bridge.service"],
            check=False,
        )
        assert res.returncode != 0, (
            f"agent-bridge must NOT be enabled: "
            f"stdout={res.stdout!r} stderr={res.stderr!r}"
        )
        # Guest ~/.ssh/config doesn't exist.
        res = p.run_in_guest(
            ["test", "-e", "/home/devm/.ssh/config"],
            check=False,
        )
        assert res.returncode != 0, "~/.ssh/config must NOT exist"
