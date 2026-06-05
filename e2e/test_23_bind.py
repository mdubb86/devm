"""23: services.X.bind exposes the port mapping on a non-default host
interface.

Default behavior: `services.X: {port: N}` publishes to 127.0.0.1
(localhost-only). Setting `services.X: {port: N, bind: "0.0.0.0:N"}`
publishes to 0.0.0.0 — visible to LAN devices.

This test asserts that the host_ip field in `sbx ports NAME --json`
reflects the user's bind choice end-to-end.
"""
import subprocess
import time

import pytest

from helpers import Shell, sbx


@pytest.mark.timeout(90)
def test_bind_exposes_on_specified_interface(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        services={
            # Polymorphic port: string form encodes the bind interface in
            # the SAME `port` field that normally takes a bare integer.
            "web": {"port": "0.0.0.0:8080"},
            # Control: another service with bare-int port → still defaults to 127.0.0.1.
            "api": {"port": 8081},
        },
    )

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)

        web_host_port = workspace.port_offset + 8080
        api_host_port = workspace.port_offset + 8081

        # Wait for both mappings to appear, then inspect host_ip.
        sbx.wait_for_port_published(
            sandbox_name, host_port=web_host_port, sandbox_port=8080, timeout=15,
        )
        sbx.wait_for_port_published(
            sandbox_name, host_port=api_host_port, sandbox_port=8081, timeout=15,
        )

        mappings = sbx.ports(sandbox_name)
        by_host_port = {m["host_port"]: m for m in mappings}

        assert by_host_port[web_host_port]["host_ip"] == "0.0.0.0", (
            f"web should bind 0.0.0.0; got {by_host_port[web_host_port]!r}"
        )
        assert by_host_port[api_host_port]["host_ip"] == "127.0.0.1", (
            f"api should default to 127.0.0.1; got {by_host_port[api_host_port]!r}"
        )

        sh.exit(timeout=30)

    # Anchor-alive: explicitly stop after shell exit.
    devm.stop(yes=True)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "stopped":
            return
        time.sleep(0.5)
    pytest.fail(f"sandbox {sandbox_name} never reached 'stopped'")
