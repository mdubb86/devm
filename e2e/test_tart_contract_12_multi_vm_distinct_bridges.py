"""Pin: every running tart VM's IP has a matching bridge* interface on
the Mac whose /24 (or wider) subnet contains it.

devm's Bug C fix (mac.HostForVM) discovers MAC_HOST by matching the
guest's IP against Mac-side bridge subnets. That works whether Apple
Virtualization gives every VM its own bridge or (as we observe on this
Mac) shares one bridge across several concurrent VMs — as long as the
guest IP is in SOME bridge's subnet.

If Apple ever creates a bridge whose subnet doesn't cover the IP tart
hands the guest — e.g. via a "netmask=/30 per VM but bridge on /24"
mismatch — HostForVM returns "no matching bridge" and iron-proxy fails
to start. This contract test would flag that at the tart layer.

What this pins:
  - Two concurrent VMs each have a valid vmnet IP.
  - For each VM, at least one bridge* subnet on the Mac contains that IP.

What it deliberately does NOT pin:
  - Whether the two VMs share a bridge or get separate ones. We
    observed both patterns depending on the spawning process; devm
    doesn't rely on separation, only on containment.
"""
from __future__ import annotations

import ipaddress
import platform
import secrets
import shutil
import socket
import subprocess
import time

import psutil
import pytest

from helpers import registry
from helpers.tart import TartSandbox


TEMPLATE = "ghcr.io/cirruslabs/debian:latest"


def _list_bridge_ipv4() -> list[tuple[str, ipaddress.IPv4Network]]:
    """Return (iface, subnet) for every bridge* interface on the Mac."""
    out = []
    for name, addrs in psutil.net_if_addrs().items():
        if not name.startswith("bridge"):
            continue
        for a in addrs:
            if a.family != socket.AF_INET:
                continue
            try:
                net = ipaddress.IPv4Network(f"{a.address}/{a.netmask}", strict=False)
            except (TypeError, ValueError):
                continue
            out.append((name, net))
    return out


@pytest.fixture
def two_running_vms():
    """Boot two independent cirruslabs debian VMs; delete on exit."""
    if platform.system() != "Darwin":
        pytest.skip("tart contract tests run on macOS only")
    if shutil.which("tart") is None:
        pytest.skip("tart not on PATH")

    subprocess.run(["tart", "pull", TEMPLATE], check=True, timeout=300)

    names = [f"contract-multi-{secrets.token_hex(2)}" for _ in range(2)]
    procs = []
    try:
        for n in names:
            registry.append("sandbox", n)
            subprocess.run(["tart", "clone", TEMPLATE, n], check=True, timeout=60)
            procs.append(subprocess.Popen(
                ["tart", "run", "--no-graphics", n],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            ))

        vms = [TartSandbox(name=n) for n in names]
        for vm in vms:
            assert vm.wait_running(timeout=120), f"{vm.name} never reached running"
            for _ in range(60):
                if vm.ip():
                    break
                time.sleep(1)
            else:
                raise RuntimeError(f"{vm.name} never got an IP")
        yield vms
    finally:
        for n, p in zip(names, procs):
            subprocess.run(["tart", "stop", n], capture_output=True, timeout=30)
            try:
                p.wait(timeout=30)
            except subprocess.TimeoutExpired:
                p.kill()
        for n in names:
            subprocess.run(["tart", "delete", n], capture_output=True, timeout=15)
            registry.remove("sandbox", n)


@pytest.mark.contract
def test_each_running_vm_has_matching_bridge(two_running_vms):
    vm_a, vm_b = two_running_vms
    ip_a = ipaddress.IPv4Address(vm_a.ip())
    ip_b = ipaddress.IPv4Address(vm_b.ip())
    assert ip_a != ip_b, f"both VMs got the same IP {ip_a}"

    bridges = _list_bridge_ipv4()
    assert bridges, "no bridge* interfaces on Mac — vmnet is down?"

    for vm_name, guest_ip in [(vm_a.name, ip_a), (vm_b.name, ip_b)]:
        matched = [(iface, subnet) for iface, subnet in bridges if guest_ip in subnet]
        assert matched, (
            f"no bridge* subnet contains {vm_name}'s IP {guest_ip}. "
            f"HostForVM would fail with 'no bridge* interface has a subnet "
            f"containing vm ip'. bridges observed: {bridges}"
        )
