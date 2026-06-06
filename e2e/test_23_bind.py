"""23: services.X.port string-form encodes the host bind interface, overriding the 127.0.0.1 default.

User declares two services: one with `port: "0.0.0.0:8080"` (LAN-
visible) and one with bare integer `port: 8081` (default). Cold-
start, then assert that `sbx ports --json` shows each mapping
bound to the user-requested interface and nothing else.

What this pins:
  - String-form `port: "0.0.0.0:N"` publishes that service on
    0.0.0.0 only.
  - Bare-int `port: N` publishes on 127.0.0.1 only (devm passes the
    explicit prefix to suppress sbx 0.30+'s implicit ::1 dual-bind).
  - Both mappings appear at the expected host_port (port_offset +
    sandbox_port).

What it doesn't cover (tested elsewhere):
  - sbx-layer interface-publish matrix:
    test_sbx_contract_12_ports_publish_interface_matrix.
  - Reachability of the published mapping over the wire:
    test_sbx_contract_14_ports_round_trip_reachable.
  - Live bind change (changing the interface on a running service):
    not yet pinned.
"""
import pytest

from helpers import Shell, sbx, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(90)
def test_bind_exposes_on_specified_interface(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        services={
            # Polymorphic port: string form encodes the bind interface in
            # the SAME `port` field that normally takes a bare integer.
            "web": {"port": "0.0.0.0:8080"},
            # Control: another service with bare-int port → still defaults
            # to 127.0.0.1 only (devm emits the explicit prefix).
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
        ips_by_host_port: dict[int, set[str]] = {}
        for m in mappings:
            ips_by_host_port.setdefault(m["host_port"], set()).add(m["host_ip"])

        assert ips_by_host_port.get(web_host_port) == {"0.0.0.0"}, (
            f"web should bind 0.0.0.0 only; got "
            f"{ips_by_host_port.get(web_host_port)!r}"
        )

        assert ips_by_host_port.get(api_host_port) == {"127.0.0.1"}, (
            f"api should default-bind to 127.0.0.1 only (devm passes the "
            f"explicit prefix to suppress 0.30+'s auto-v6); got "
            f"{ips_by_host_port.get(api_host_port)!r}"
        )

        sh.exit(timeout=30)

    # Anchor-alive: explicitly stop after shell exit.
    stop_and_wait_stopped(devm, sandbox_name)
