"""ports: --publish interface-binding matrix.

For default publishes (no IP prefix), sbx 0.30+ binds BOTH 127.0.0.1
and ::1. For explicit IP prefixes, only the requested family is bound.

Parametrized over the three forms we care about:
  * "127.0.0.1:H:S" -> exactly {"127.0.0.1"}
  * "0.0.0.0:H:S"   -> exactly {"0.0.0.0"}
  * "H:S" bare      -> exactly {"127.0.0.1", "::1"}

Asserts via the `host_ip` field set in `sbx ports --json` (not by
actually connecting — that's P3).

Devm dependency: internal/orchestrator/ports.go publishSpec emits
"127.0.0.1:H:S" explicitly to land on the first row. internal/orchestrator/
apply_live.go also uses the explicit form for the live-reconcile path.
The bare form behavior is documented but devm doesn't use it; we
assert it anyway so a future sbx change to "bare publishes v4 only"
fires the test (which would indicate the IPv6 stack is no longer
opt-in by default).
"""
from __future__ import annotations

import subprocess

import pytest

from helpers import sbx
from helpers.contract import contract_sandbox, minimal_kit

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(180)
@pytest.mark.parametrize("spec, expected_ips", [
    ("127.0.0.1:55001:8080", {"127.0.0.1"}),
    ("0.0.0.0:55002:8081", {"0.0.0.0"}),
    ("55003:8082", {"127.0.0.1", "::1"}),
], ids=["v4-only", "0.0.0.0", "bare-v4-and-v6"])
def test_publish_interface_binding(sandbox_name, spec, expected_ips):
    with contract_sandbox(minimal_kit(), sandbox_name):
        r = subprocess.run(
            ["sbx", "ports", sandbox_name, "--publish", spec],
            capture_output=True, timeout=15,
        )
        assert r.returncode == 0, (
            f"publish {spec} failed: {r.stderr.decode()}"
        )

        parts = spec.split(":")
        host_port = int(parts[-2])

        ips = {
            m["host_ip"]
            for m in sbx.ports(sandbox_name)
            if m["host_port"] == host_port
        }
        assert ips == expected_ips, (
            f"publish {spec}: expected host_ip set {expected_ips}, got {ips}"
        )
