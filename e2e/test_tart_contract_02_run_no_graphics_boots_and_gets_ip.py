"""Pin: `tart run --no-graphics NAME` boots and produces a vmnet IP.

This is the boot cycle the daemon's supervisor depends on. If
`--no-graphics` ever stops working OR boot stops producing an IP,
every cold-start in devm breaks.
"""
import secrets
import subprocess
import time

import pytest

from helpers import registry
from helpers.tart import TartSandbox


@pytest.mark.devm
def test_tart_run_no_graphics_boots():
    template = "ghcr.io/cirruslabs/debian:latest"
    name = f"contract-boot-{secrets.token_hex(2)}"
    registry.append("sandbox", name)
    proc = None
    try:
        subprocess.run(["tart", "pull", template], check=True, timeout=300)
        subprocess.run(["tart", "clone", template, name], check=True, timeout=60)

        proc = subprocess.Popen(
            ["tart", "run", "--no-graphics", name],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )

        vm = TartSandbox(name=name)
        assert vm.wait_running(timeout=120), f"{name} never reached running"

        ip = ""
        for _ in range(60):
            ip = vm.ip()
            if ip:
                break
            time.sleep(1)
        assert ip, f"{name} never got an IP"
        # vmnet range is 192.168.64.0/24 by default.
        assert ip.startswith("192.168."), f"unexpected IP: {ip}"
    finally:
        subprocess.run(["tart", "stop", name], capture_output=True, timeout=30)
        if proc:
            try:
                proc.wait(timeout=30)
            except subprocess.TimeoutExpired:
                proc.kill()
        subprocess.run(["tart", "delete", name],
                       capture_output=True, timeout=10)
        registry.remove("sandbox", name)
