"""ssh: feature — the Mac-side jump allowlist rejects (host, port)
pairs not derived from the project's ssh: list.

Why this shape: the guest's ssh_config only carries Host blocks for
listed aliases, so a plain `ssh <unlisted>` falls back to default
DNS resolution and never reaches the jump — that's a UX layer, not
a security boundary. A compromised guest can override the client
config at will; the jump's direct-tcpip allowlist is what stops it.
This test crafts the exact invocation such an attacker would — a
`-o ProxyJump=devm-jump` to a (host, port) not in the project's
list — and asserts the jump rejects with "Prohibited".
"""
from __future__ import annotations

import pytest

from helpers import e2e_project, local_sshd_target

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(1500)
def test_jump_denies_untrusted():
    with e2e_project("ssh-deny") as p, local_sshd_target() as alias_allowed:
        p.set_devm_yaml({"ssh": [alias_allowed]})
        p.start()
        # Baseline: the allowed alias actually works — otherwise a
        # failure on the attack probe could just mean the jump is
        # broken for every destination.
        ok = p.run_in_guest(["ssh", alias_allowed, "true"], check=False)
        assert ok.returncode == 0, (
            f"baseline allowed alias must work: "
            f"stdout={ok.stdout!r} stderr={ok.stderr!r}"
        )
        # Attack: force ssh through devm-jump to 127.0.0.1:1 (not in
        # the allowlist). The jump authenticates successfully, then
        # rejects the direct-tcpip request.
        res = p.run_in_guest(
            ["ssh",
             "-o", "ProxyJump=devm-jump",
             "-o", "HostName=127.0.0.1",
             "-o", "Port=1",
             "-o", "StrictHostKeyChecking=no",
             "-o", "UserKnownHostsFile=/dev/null",
             "-o", "PreferredAuthentications=publickey",
             "-o", "ConnectTimeout=5",
             "unlisted-destination",
             "true"],
            check=False,
        )
        assert res.returncode != 0, "jump must deny unlisted (host, port)"
        stderr_l = res.stderr.lower()
        assert (
            "administratively prohibited" in stderr_l
            or "channel open failed" in stderr_l
            or "channel request failed" in stderr_l
        ), f"want jump-level reject in stderr; got {res.stderr!r}"
