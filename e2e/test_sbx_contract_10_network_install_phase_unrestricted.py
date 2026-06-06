"""network: install: phase has APT access regardless of allowedDomains.

Verified empirically: sbx routes install: HTTP through a network policy
proxy. APT mirrors are excepted (the proxy lets ubuntu/debian repos
through even when those domains aren't in allowedDomains). Arbitrary
HTTP to non-allowed domains returns an HTTP 200 'Blocked by network
policy' page (so curl rc=0 but the body is the block notice — easy
trap).

This contract pins ONLY the APT exemption, because that's the property
devm depends on: bootstrap.sh (when re-enabled) uses apt-get, and the
user's own install: commonly does `apt-get install -y X`. We do NOT
assert anything about arbitrary curl in install:.

Devm dependency: bootstrap.sh + typical user install: lines like
`apt-get install -y ncurses-term, jq, etc.` all work even when the
project's allowedDomains is tight.
"""
from __future__ import annotations

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(180)
def test_install_phase_apt_works_under_tight_policy(sandbox_name):
    # Tight allow-list (github.com only). Install runs apt-get update —
    # the apt mirrors are NOT in allowedDomains. If sbx ever removes
    # the install-phase apt exception, this test fires.
    spec = minimal_kit(
        install=["apt-get update -qq"],
        network_allowed=["github.com"],
    )
    # If apt-get update failed, sbx run would exit non-zero, sandbox
    # would never reach running, and contract_sandbox would raise. So
    # reaching the body of the with-block IS the assertion. Belt:
    # verify apt actually populated package lists (lists dir is empty
    # on a fresh image until apt-get update fetches anything).
    with contract_sandbox(spec, sandbox_name):
        r = sbx_exec(sandbox_name, "sh", "-c",
                     "ls /var/lib/apt/lists/ | grep -v lock | wc -l")
        assert r.returncode == 0, f"ls failed: {r.stderr.decode()}"
        count = int(r.stdout.decode().strip())
        assert count > 0, (
            f"apt lists dir is empty after install: 'apt-get update' — apt "
            f"may not have actually fetched anything (count={count})"
        )
