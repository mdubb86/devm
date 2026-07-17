"""Contract e2e: the PRODUCTION `devm` binary, exec'd as `softnet` (via a
symlink, exactly as tart resolves `--net-softnet` on $PATH), driven purely
through the control-socket protocol — proving Plan 2's host<->guest INGRESS
(`setExposeMap`) and UDP/NTP EGRESS forward (`udp:123` under ENFORCED),
alongside DNS coexistence.

Companion to e2e/test_softnet_15_production_egress.py (Plan 1's three-state
TCP egress state machine). This test drives the two Plan 2 features:

  - Ingress: a host-side listener opened via `setExposeMap` accepts a
    connection and softnet dials `GuestLeaseIP:guest_port` inside its gvisor
    netstack, splicing host<->guest — proving direct-service / SSH-style
    host->guest reachability without a host-routable guest IP.
  - NTP egress: under ENFORCED, guest UDP datagrams to dport 123 (any dest
    IP, mirroring dport-based DNAT) are forwarded to the configured
    `iron_proxy.ntp` endpoint — proving post-sleep clock-heal reachability.
  - DNS coexistence: while the UDP forwarder is active, softnet's own
    resolver still answers the `devm.test.` zone locally, proving the UDP
    forward didn't shadow the bound gateway:53 DNS endpoint.

Plain cirruslabs/debian clone (NOT devm-base): devm-base's default-drop
nftables egress lock is scoped to the vmnet gateway and would sabotage egress
under softnet's distinct 192.168.127.0/24 gateway for reasons unrelated to
the mechanisms under test — same rationale as the fixture contract test and
test_softnet_15.
"""
import os
import secrets
import subprocess
import time
from pathlib import Path

import pytest

from helpers import registry
from test_tart_contract_14_softnet import (
    _free_port,
    _serve_udp,
    _tart,
    _state,
    _wait_ip,
    _gexec,
    _pingpong,
)
from test_softnet_15_production_egress import _control, _cleanup

REPO_ROOT = Path(__file__).parent.parent
TEMPLATE = "ghcr.io/cirruslabs/debian:latest"
NAT_ALIAS = "192.168.127.254"
SOFTNET_SUBNET_PREFIX = "192.168.127."


@pytest.mark.contract
def test_softnet_production_ingress_and_ntp():
    # --- build the PRODUCTION binary and symlink it as `softnet` on $PATH ---
    bindir = Path(
        subprocess.run(
            ["mktemp", "-d", "-t", "softnet-prod-ingress-bin"],
            capture_output=True, text=True, check=True,
        ).stdout.strip()
    )
    devm_bin = bindir / "devm"
    softnet_bin = bindir / "softnet"
    build = subprocess.run(
        ["go", "build", "-o", str(devm_bin), "./cmd/devm"],
        cwd=REPO_ROOT, capture_output=True, text=True, timeout=180,
    )
    assert build.returncode == 0, f"go build devm failed:\n{build.stderr}"
    softnet_bin.symlink_to(devm_bin)

    host_port = _free_port()   # softnet's host-side ingress listener
    guest_port = 15432         # guest echo server (direct-service-like)

    # --- host UDP echo stands in for devm's host NTP responder ---
    udp_port = _free_port()
    _serve_udp(udp_port)
    udp_addr = f"127.0.0.1:{udp_port}"

    sock_path = str(bindir / "control.sock")
    env = dict(os.environ)
    env["PATH"] = str(bindir) + os.pathsep + env["PATH"]
    env["SOFTNET_CONTROL_SOCK"] = sock_path

    name = f"e2e-softnet-prod-ing-{secrets.token_hex(2)}"
    registry.append("sandbox", name)
    proc = None
    logf = open(bindir / "softnet.log", "w")
    try:
        _tart("clone", TEMPLATE, name, timeout=90)

        proc = subprocess.Popen(
            ["tart", "run", "--no-graphics", "--net-softnet", name],
            stdout=logf, stderr=subprocess.STDOUT, env=env,
        )

        # --- wait for the VM to boot ---
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline and _state(name) != "running":
            if proc.poll() is not None:
                log = (bindir / "softnet.log").read_text()
                raise AssertionError(f"tart run exited early rc={proc.returncode}:\n{log}")
            time.sleep(2)
        assert _state(name) == "running", "VM never reached running"

        # --- production netstack is the sole NIC: guest IP from OUR DHCP ---
        ip = _wait_ip(name)
        assert ip.startswith(SOFTNET_SUBNET_PREFIX), f"guest IP {ip} not from our DHCP"

        # --- control socket must exist (softnet creates it at startup) ---
        assert _control(sock_path, {
            "op": "setExposeMap",
            "expose": [
                {"guest_port": guest_port, "bind_ip": "127.0.0.1", "host_port": host_port},
            ],
        }), (
            f"never connected to control socket {sock_path}; log:\n"
            f"{(bindir / 'softnet.log').read_text()}"
        )

        # --- guest-side echo server (socat ships in cirruslabs/debian) ---
        launch = _gexec(
            name,
            f"nohup socat TCP-LISTEN:{guest_port},fork,reuseaddr EXEC:/bin/cat "
            ">/dev/null 2>&1 & echo LAUNCHED",
        )
        assert "LAUNCHED" in launch.stdout, f"echo server launch: {launch.stdout!r}"

        # --- INGRESS: from the Mac, round-trip a nonce THROUGH softnet's
        # host listener -> netstack -> guest echo. Retries absorb listener /
        # guest-echo / ARP startup races (positive assertion). ---
        nonce_in = secrets.token_hex(8)
        got_in = _pingpong("127.0.0.1", host_port, nonce_in)
        assert got_in == nonce_in, f"ingress ping-pong failed: sent {nonce_in!r} got {got_in!r}"

        # --- NTP: flip to ENFORCED with the host UDP echo as the ntp endpoint ---
        ok = _control(
            sock_path,
            {
                "op": "setPolicy",
                "policy": "ENFORCED",
                "iron_proxy": {
                    "http": udp_addr,
                    "https": udp_addr,
                    "dns": udp_addr,
                    "ntp": udp_addr,
                },
            },
        )
        assert ok, "failed to send ENFORCED setPolicy"

        # --- guest UDP datagram to dport 123 (arbitrary dest IP) -> host echo ---
        nonce_udp = secrets.token_hex(8)
        py = (
            "python3 -c \"import socket;"
            "s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.settimeout(4);"
            f"s.sendto(b'{nonce_udp}',('1.2.3.4',123));"
            "print(s.recvfrom(2048)[0].decode())\""
        )
        last = None
        for _ in range(6):
            last = _gexec(name, py)
            if nonce_udp in last.stdout:
                break
            time.sleep(1)
        assert nonce_udp in last.stdout, (
            f"udp :123 round-trip failed: {last.stdout!r} / {last.stderr!r}"
        )

        # --- DNS coexistence: the UDP forwarder must not shadow the bound
        # gateway:53 DNS endpoint — softnet's devm.test zone still resolves. ---
        dns = None
        for _ in range(6):
            dns = _gexec(name, "getent hosts allowed.devm.test | awk '{print $1}'")
            if NAT_ALIAS in dns.stdout:
                break
            time.sleep(1)
        assert NAT_ALIAS in dns.stdout, f"softnet DNS broke under UDP fwd: {dns.stdout!r}"
    finally:
        _cleanup(name, proc, softnet_bin)
        logf.close()
        registry.remove("sandbox", name)
