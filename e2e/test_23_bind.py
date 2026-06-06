"""23: services.X.bind exposes the port mapping on a non-default host
interface.

Default behavior: `services.X: {port: N}` publishes to BOTH loopback
stacks (127.0.0.1 AND ::1) under sbx 0.30+ ("Bind both loopback
stacks by default when publishing ports" — v0.30.0 release notes).
Setting `services.X: {port: "0.0.0.0:N"}` publishes to 0.0.0.0
— visible to LAN devices.

This test asserts that the host_ip field in `sbx ports NAME --json`
reflects the user's bind choice end-to-end.
"""
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
            # Control: another service with bare-int port → default-binds to
            # BOTH 127.0.0.1 and ::1 under sbx 0.30+.
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
        # Group by host_port — there may be MULTIPLE entries per host_port
        # since sbx 0.30 binds both v4 and v6 stacks on default publish.
        ips_by_host_port: dict[int, set[str]] = {}
        for m in mappings:
            ips_by_host_port.setdefault(m["host_port"], set()).add(m["host_ip"])

        # web: explicit 0.0.0.0 → single mapping on 0.0.0.0 (no v6 stack
        # added because the user named an explicit IP).
        assert ips_by_host_port.get(web_host_port) == {"0.0.0.0"}, (
            f"web should bind 0.0.0.0 only; got "
            f"{ips_by_host_port.get(web_host_port)!r}"
        )

        # api: default port → BOTH loopback stacks (v0.30 behavior).
        api_ips = ips_by_host_port.get(api_host_port, set())
        assert api_ips == {"127.0.0.1", "::1"}, (
            f"api default-bind should be on both v4 and v6 loopback "
            f"(sbx 0.30+); got {api_ips!r}"
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
