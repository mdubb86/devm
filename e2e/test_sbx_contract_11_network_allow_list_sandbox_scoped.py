"""network: allow-lists are sandbox-scoped, not host-global.

Two sandboxes A and B both have only github.com allowed. Adding
example.com to A's policy must NOT make example.com reachable from B.

Baseline-then-perturb pattern:
  1. Bring up A and B with identical restrictive policies
  2. Verify neither reaches example.com
  3. `sbx policy allow network A example.com`
  4. Verify A reaches example.com, B still blocks

Devm dependency: two devm projects running in parallel must have
isolated runtime allow-lists. If sbx ever regresses to host-global
policy (the pre-0.29 behavior), one devm project could mutate
another's effective policy invisibly.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract

DOMAIN = "example.com"


def _can_reach(sandbox: str) -> bool:
    """Real reachability — non-allowed sbx fetches return a 'Blocked by
    network policy' page with HTTP 200, so we check the BODY not just
    the exit code."""
    r = sbx_exec(
        sandbox, "curl", "-fsS", "--max-time", "10",
        f"https://{DOMAIN}",
        timeout=20,
    )
    if r.returncode != 0:
        return False
    body = r.stdout.decode(errors="replace").lower()
    return "blocked by network policy" not in body


@pytest.mark.timeout(240)
def test_allow_list_is_sandbox_scoped(sandbox_name):
    name_a = f"{sandbox_name}-a"
    name_b = f"{sandbox_name}-b"
    spec = minimal_kit(network_allowed=["github.com"])

    with contract_sandbox(spec, name_a):
        with contract_sandbox(spec, name_b):
            # Baseline.
            assert not _can_reach(name_a), (
                f"baseline failed: A should not reach {DOMAIN} before policy add"
            )
            assert not _can_reach(name_b), (
                f"baseline failed: B should not reach {DOMAIN} before policy add"
            )

            # Perturb A only.
            r = subprocess.run(
                ["sbx", "policy", "allow", "network", name_a, DOMAIN],
                capture_output=True, timeout=15,
            )
            assert r.returncode == 0, (
                f"policy allow failed: {r.stderr.decode()}"
            )

            # Assert: A reaches, B still blocked (the isolation property).
            assert _can_reach(name_a), (
                f"after policy add, A should reach {DOMAIN}"
            )
            assert not _can_reach(name_b), (
                f"B should still block {DOMAIN} — policy add must be sandbox-scoped, "
                f"NOT host-global"
            )
