"""network: allowedDomains in spec.yaml is enforced.

A sandbox declared with `network.allowedDomains: [github.com]` can
reach github.com but cannot reach example.com. The policy is
applied at create time, not via runtime add (that's N2).

Devm dependency: internal/render/spec.go renders devm.yaml's
network.allowed_domains into the kit's network.allowedDomains. If
enforcement breaks, devm.yaml's allowed_domains becomes a no-op and
the security property the user thinks they're getting is gone.
"""
from __future__ import annotations

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


def _can_reach(sandbox: str, domain: str) -> bool:
    """True if curl from inside the sandbox can fetch https://<domain>."""
    r = sbx_exec(
        sandbox, "curl", "-fsS", "-o", "/dev/null", "--max-time", "10",
        f"https://{domain}",
        timeout=20,
    )
    return r.returncode == 0


@pytest.mark.timeout(180)
def test_allowed_domains_in_spec_yaml_is_enforced(sandbox_name):
    spec = minimal_kit(network_allowed=["github.com"])
    with contract_sandbox(spec, sandbox_name):
        assert _can_reach(sandbox_name, "github.com"), (
            "github.com was declared in allowedDomains; should be reachable"
        )
        assert not _can_reach(sandbox_name, "example.com"), (
            "example.com was NOT in allowedDomains; should be blocked"
        )
