"""Pin: `tart ip <name>` returns a single-line IPv4 address in the guest's
vmnet subnet.

devm's Bug C fix (HostForVM) queries `tart ip <vm>` to discover which
bridge subnet the guest lives on, then binds iron-proxy on that bridge.
If tart ever changes the `ip` output shape — a JSON blob, multiple lines,
IPv6 first, an empty string on race, etc. — HostForVM silently returns
the wrong bridge and every network call from the guest breaks.

This contract test surfaces such a change at the tart layer instead of as
a mysterious "guest can't reach network" symptom in devm.

What this pins:
  - `tart ip <name>` on a running VM exits 0.
  - Stdout is exactly one non-empty line.
  - The line parses as an IPv4 address (net.parseaddr(...).version == 4).
  - The address falls in one of the Apple Virtualization vmnet ranges
    (192.168.0.0/16 — we've observed 192.168.64.0/24, 192.168.97.0/24,
    192.168.139.0/23; asserting any 192.168/16 keeps the pin resilient
    to Apple picking a new /24 without breaking on a real regression).
"""
from __future__ import annotations

import ipaddress
import subprocess

import pytest


@pytest.mark.contract
def test_tart_ip_returns_single_line_vmnet_ipv4(inspector_vm):
    r = subprocess.run(
        ["tart", "ip", inspector_vm.name],
        capture_output=True, timeout=10,
    )
    assert r.returncode == 0, (
        f"tart ip exited non-zero: rc={r.returncode} "
        f"stderr={r.stderr.decode()!r}"
    )

    out = r.stdout.decode()
    lines = [ln for ln in out.splitlines() if ln.strip()]
    assert len(lines) == 1, (
        f"expected exactly one non-empty output line; got {lines!r}"
    )

    ip_str = lines[0].strip()
    try:
        addr = ipaddress.ip_address(ip_str)
    except ValueError as e:
        pytest.fail(f"tart ip output {ip_str!r} is not a valid IP: {e}")

    assert isinstance(addr, ipaddress.IPv4Address), (
        f"tart ip returned an IPv6 address ({ip_str}); devm's HostForVM "
        f"expects IPv4."
    )

    vmnet_range = ipaddress.IPv4Network("192.168.0.0/16")
    assert addr in vmnet_range, (
        f"tart ip returned {ip_str} — outside the 192.168/16 range Apple "
        f"Virtualization historically uses for vmnet. If Apple has picked "
        f"a new range, devm's bridge discovery may need to widen."
    )
