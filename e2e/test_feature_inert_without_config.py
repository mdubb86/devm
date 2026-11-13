"""Project without ssh: list → feature inert.

No agent-bridge service, no guest ~/.ssh/config, no Mac loopback
jump listener, no softnet NAT entry for a jump port. Pins the
"opt-in via project.ssh" contract: a project that doesn't ask for
the feature doesn't pay any of its costs.
"""
from __future__ import annotations

import pytest

from helpers import e2e_project

pytestmark = pytest.mark.devm

# softnet's NAT alias (the host loopback the guest reaches the Mac
# through — internal/softnet/contract.go NATAliasIP). With a policy
# of FORWARDING and NO jump/bridge in MacPorts, softnet RSTs every
# port the project hasn't declared.
_NAT_ALIAS = "192.168.127.254"

# Sentinel ports sprayed across the macOS ephemeral range (49152-
# 65535 by default). If the feature were live, exactly one of
# devm-jump's dynamically-chosen ports would respond; with the
# feature inert the whole range RSTs via softnet's undeclared-port
# rule (internal/softnet/egress_test.go `undeclared_rst` case).
_SENTINEL_PORTS = (49222, 51717, 55123, 58777, 61321, 64501)


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_feature_inert():
    with e2e_project("ssh-inert") as p:
        # No ssh: key in devm.yaml — the helper's default write omits it.
        # netcat-openbsd + Debian mirror allow is needed for the (c)+(d)
        # from-guest probe; the base image doesn't ship nc. Pattern
        # mirrors test_72_systemd_service.py:44-45.
        p.set_devm_yaml({
            "packages": ["netcat-openbsd"],
            "network": {"allow": ["deb.debian.org", "security.debian.org"]},
        })
        p.start()

        # (a) Guest-side agent-bridge unit NOT enabled.
        res = p.run_in_guest(
            ["systemctl", "is-enabled", "devm-ssh-agent-bridge.service"],
            check=False,
        )
        assert res.returncode != 0, (
            f"agent-bridge must NOT be enabled: "
            f"stdout={res.stdout!r} stderr={res.stderr!r}"
        )

        # (b) Guest ~/.ssh/config does not exist.
        res = p.run_in_guest(
            ["test", "-e", "/home/devm/.ssh/config"],
            check=False,
        )
        assert res.returncode != 0, "~/.ssh/config must NOT exist"

        # (c)+(d) Mac loopback jump listener NOT bound AND softnet has
        # no NAT entry for one. From the guest, probe the NAT alias on
        # a handful of ports across the ephemeral range. If either (c)
        # or (d) were violated, at least one probe would succeed.
        #
        # Preflight: fail loud if `nc` isn't actually installed. Without
        # it, `timeout nc …` returns 127, the loop's `exit=0 not in
        # stdout` is trivially true, and the test passes silently
        # even with the feature live.
        pre = p.run_in_guest(["which", "nc"], check=False)
        assert pre.returncode == 0 and pre.stdout.strip(), (
            f"nc missing in guest — packages install failed or the "
            f"base image dropped netcat-openbsd. probe would silently "
            f"pass; aborting. stdout={pre.stdout!r} stderr={pre.stderr!r}"
        )
        for port in _SENTINEL_PORTS:
            res = p.run_in_guest(
                ["sh", "-c",
                 f"timeout 2 nc -z {_NAT_ALIAS} {port}; echo exit=$?"],
                check=False,
            )
            assert "exit=0" not in res.stdout, (
                f"{_NAT_ALIAS}:{port} unexpectedly reachable with "
                f"ssh: feature inert — either the jump listener is "
                f"bound (c) or softnet admits the port (d). "
                f"stdout={res.stdout!r} stderr={res.stderr!r}"
            )
