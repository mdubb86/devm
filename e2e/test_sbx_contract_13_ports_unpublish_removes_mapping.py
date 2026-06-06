"""ports: --unpublish IP:H:S removes the matching mapping; --json reflects.

Publish 127.0.0.1:55101:8080. Verify it appears in --json. Unpublish
127.0.0.1:55101:8080. Verify it disappears.

Devm dependency: internal/orchestrator/apply_live.go KindPortRemove
and KindPortChange call --unpublish with the IP-prefixed form. If
sbx changed --unpublish's matching semantics (e.g. IP-prefixed no
longer matches), devm's live port-remove would silently fail and
the stale mapping would persist (yesterday's test_12 failure shape).
"""
from __future__ import annotations

import subprocess

import pytest

from helpers import sbx
from helpers.contract import contract_sandbox, minimal_kit

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(120)
def test_unpublish_removes_matching_mapping(sandbox_name):
    SPEC = "127.0.0.1:55101:8080"
    HOST_PORT = 55101
    SANDBOX_PORT = 8080

    with contract_sandbox(minimal_kit(), sandbox_name):
        r = subprocess.run(
            ["sbx", "ports", sandbox_name, "--publish", SPEC],
            capture_output=True, timeout=15,
        )
        assert r.returncode == 0, f"publish failed: {r.stderr.decode()}"

        sbx.wait_for_port_published(
            sandbox_name, host_port=HOST_PORT, sandbox_port=SANDBOX_PORT, timeout=10,
        )
        ips_before = {
            m["host_ip"] for m in sbx.ports(sandbox_name)
            if m["host_port"] == HOST_PORT
        }
        assert ips_before == {"127.0.0.1"}, f"publish before unpublish: {ips_before}"

        r = subprocess.run(
            ["sbx", "ports", sandbox_name, "--unpublish", SPEC],
            capture_output=True, timeout=15,
        )
        assert r.returncode == 0, f"unpublish failed: {r.stderr.decode()}"

        sbx.wait_for_port_absent(
            sandbox_name, host_port=HOST_PORT, sandbox_port=SANDBOX_PORT, timeout=10,
        )
