"""network: sbx policy allow network <sandbox> <domain> adds at runtime.

Establish a sandbox where example.com is blocked. Run `sbx policy
allow network <sandbox> example.com`. Assert example.com is now
reachable from inside, immediately (no restart).

Baseline-then-perturb: verify blocked first, then add policy, then
verify reachable.

Devm dependency: internal/orchestrator/apply_live.go handles
KindNetworkAdd by running `sbx policy allow network sb.Name domain`.
sbx 0.29+ requires the sandbox name as a scope arg (we just fixed
this); if sbx changes the syntax again, apply_live breaks.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract

DOMAIN = "example.com"


def _can_reach(sandbox: str) -> bool:
    r = sbx_exec(
        sandbox, "curl", "-fsS", "-o", "/dev/null", "--max-time", "10",
        f"https://{DOMAIN}",
        timeout=20,
    )
    return r.returncode == 0


@pytest.mark.timeout(180)
def test_runtime_policy_add_takes_effect(sandbox_name):
    spec = minimal_kit(network_allowed=["github.com"])  # example.com NOT allowed
    with contract_sandbox(spec, sandbox_name):
        # Baseline.
        assert not _can_reach(sandbox_name), (
            f"baseline failed: {DOMAIN} should be blocked before policy add"
        )

        # Perturb.
        r = subprocess.run(
            ["sbx", "policy", "allow", "network", sandbox_name, DOMAIN],
            capture_output=True, timeout=15,
        )
        assert r.returncode == 0, (
            f"sbx policy allow failed: rc={r.returncode}\nstderr={r.stderr.decode()}"
        )

        # Assert.
        assert _can_reach(sandbox_name), (
            f"after policy add, {DOMAIN} should be reachable; still blocked"
        )
