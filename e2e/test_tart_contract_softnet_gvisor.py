"""De-risking SPIKE: prove the whole tart -> userspace-softnet -> gvisor loop,
end to end, on a real VM, as the UNPRIVILEGED user (no sudo, no SUID).

This is NOT a test of devm's production code — it drives `tart` directly and
builds its own throwaway `softnet` binary (from ../e2e/spike, an isolated Go
module that embeds gvisor-tap-vsock and is deliberately kept out of devm's
production go.mod). The point is a truthful yes/no on feasibility:

  tart run --net-softnet
    -> tart execs a $PATH binary literally named `softnet` with
       `--vm-fd 0 --vm-mac-address <mac>`, handing it one end of a
       socketpair(AF_UNIX, SOCK_DGRAM) as stdin (fd 0), each datagram a raw
       Ethernet II frame
    -> our softnet feeds those frames into an embedded gvisor-tap-vsock netstack
       that runs userspace DHCP/ARP/DNS and forwards outbound TCP only to an
       allowlisted target
    -> the guest gets an IP from *our* DHCP on its sole NIC and can reach an
       ALLOWED host but NOT a forbidden one, with softnet at euid != 0.

Uses a plain cirruslabs/debian clone (NOT devm-base): devm-base bakes in a
default-drop nftables egress lock scoped to the vmnet gateway, which would
sabotage egress under softnet's 192.168.127.0/24 gateway for reasons unrelated
to the mechanism under test.
"""
import http.server
import json
import os
import secrets
import socket
import socketserver
import subprocess
import threading
import time
from pathlib import Path

import pytest

from helpers import registry

SPIKE_DIR = Path(__file__).parent / "spike"
TEMPLATE = "ghcr.io/cirruslabs/debian:latest"
NAT_ALIAS = "192.168.127.254"  # our gvisor gateway NATs this to host 127.0.0.1
SOFTNET_SUBNET_PREFIX = "192.168.127."

SOCK_DGRAM = 2  # darwin SO_TYPE value
AF_UNIX = 1


def _free_port() -> int:
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def _serve(port: int, body: bytes) -> socketserver.TCPServer:
    class H(http.server.BaseHTTPRequestHandler):
        def do_GET(self):
            self.send_response(200)
            self.end_headers()
            self.wfile.write(body)

        def log_message(self, *a):
            pass

    httpd = socketserver.TCPServer(("127.0.0.1", port), H)
    threading.Thread(target=httpd.serve_forever, daemon=True).start()
    return httpd


def _tart(*args, timeout=30):
    return subprocess.run(
        ["tart", *args], capture_output=True, text=True, timeout=timeout
    )


def _state(name: str) -> str:
    r = _tart("list", "--format=json", timeout=10)
    try:
        for e in json.loads(r.stdout):
            if e.get("Name") == name:
                return e.get("State", "")
    except Exception:
        pass
    return "absent"


@pytest.mark.contract
def test_tart_contract_softnet_gvisor():
    # --- build the throwaway softnet binary into a temp dir on $PATH ---
    bindir = Path(
        subprocess.run(
            ["mktemp", "-d", "-t", "softnet-spike-bin"],
            capture_output=True, text=True, check=True,
        ).stdout.strip()
    )
    softnet_bin = bindir / "softnet"
    build = subprocess.run(
        ["go", "build", "-o", str(softnet_bin), "."],
        cwd=SPIKE_DIR, capture_output=True, text=True, timeout=180,
    )
    assert build.returncode == 0, f"go build softnet failed:\n{build.stderr}"
    assert softnet_bin.exists()

    marker_path = bindir / "marker.json"
    allow_port = _free_port()
    block_port = _free_port()
    _serve(allow_port, b"ALLOWED-OK")
    _serve(block_port, b"FORBIDDEN-OK")

    env = dict(os.environ)
    env["PATH"] = str(bindir) + os.pathsep + env["PATH"]
    env["SPIKE_ALLOW"] = f"127.0.0.1:{allow_port}"  # post-NAT dial target
    env["SPIKE_MARKER"] = str(marker_path)

    name = f"e2e-spike-softnet-{secrets.token_hex(2)}"
    registry.append("sandbox", name)
    proc = None
    logf = open(bindir / "softnet.log", "w")
    try:
        _tart("clone", TEMPLATE, name, timeout=90)

        # Non-interactive launch (stdout NOT a tty) so tart never attempts the
        # SUID/sudo setup path for softnet — the crux of root-free operation.
        proc = subprocess.Popen(
            ["tart", "run", "--no-graphics", "--net-softnet", name],
            stdout=logf, stderr=subprocess.STDOUT, env=env,
        )

        # --- the marker proves the tart<->softnet contract + euid ---
        mk = _wait_marker(marker_path, proc, bindir)

        # Contract: tart execd OUR softnet with the pinned argv.
        assert Path(mk["argv"][0]).name == "softnet"
        assert mk["argv"][1:3] == ["--vm-fd", "0"], mk["argv"]
        assert mk["argv"][3] == "--vm-mac-address"
        vm_mac = mk["vm_mac"]
        assert vm_mac and ":" in vm_mac, vm_mac
        assert mk["vm_fd"] == 0

        # Wire protocol: stdin is an AF_UNIX SOCK_DGRAM socket.
        assert mk["sock_type"] == SOCK_DGRAM, f"SO_TYPE={mk['sock_type']}"
        assert mk["sock_family"] == AF_UNIX, f"family={mk['sock_family']}"

        # Root-free: softnet runs at the invoking user's euid, not root.
        assert mk["euid"] != 0, f"softnet euid={mk['euid']} (expected non-root)"
        assert mk["uid"] != 0
        assert mk["spike_allow"] == [f"127.0.0.1:{allow_port}"]

        # --- wait for the VM to boot ---
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline and _state(name) != "running":
            time.sleep(2)
        assert _state(name) == "running", "VM never reached running"

        # --- sole NIC + IP via our userspace DHCP ---
        nics = _wait_nic(name)
        assert nics == ["enp0s1"], f"expected exactly one NIC, got {nics}"

        ip = _wait_ip(name)
        assert ip.startswith(SOFTNET_SUBNET_PREFIX), f"guest IP {ip} not from our DHCP"

        # default route points at our gvisor gateway
        route = _gexec(name, "ip route show default")
        assert "192.168.127.1" in route.stdout, route.stdout

        # --- enforcement: ALLOWED succeeds, FORBIDDEN fails ---
        allowed = _gexec(
            name,
            f"curl -s -m 8 -o /dev/null -w '%{{http_code}}' http://{NAT_ALIAS}:{allow_port}/",
        )
        assert allowed.stdout.strip() == "200", f"allowed curl: {allowed.stdout!r}"

        # ALLOWED via our gvisor DNS (allowed.spike.test -> NAT alias)
        via_dns = _gexec(
            name,
            f"curl -s -m 8 -o /dev/null -w '%{{http_code}}' http://allowed.spike.test:{allow_port}/",
        )
        assert via_dns.stdout.strip() == "200", f"dns curl: {via_dns.stdout!r}"

        # FORBIDDEN host-port (same NAT alias, not in allowlist) -> dropped
        blocked = _gexec(
            name,
            f"curl -s -m 8 -o /dev/null -w '%{{http_code}}' http://{NAT_ALIAS}:{block_port}/ ; echo RC=$?",
        )
        assert "RC=0" not in blocked.stdout, f"forbidden host-port reachable: {blocked.stdout!r}"

        # FORBIDDEN open internet -> dropped by our forward policy
        internet = _gexec(
            name,
            "curl -s -m 8 -o /dev/null -w '%{http_code}' http://1.1.1.1/ ; echo RC=$?",
        )
        assert "RC=0" not in internet.stdout, f"internet reachable: {internet.stdout!r}"

        # raw Ethernet framing: the first guest frame's src MAC == --vm-mac-address
        mk2 = json.load(open(marker_path))
        assert mk2["first_src_mac"] == vm_mac, (mk2["first_src_mac"], vm_mac)

        # --- stretch: a root guest cannot open a second egress path ---
        # There is no second NIC to bring up, and flushing/adding routes can't
        # manufacture one; egress stays caged at the host socketpair boundary.
        esc = _gexec(
            name,
            "sudo ip link add dummy0 type dummy 2>&1; "
            "sudo ip route add default via 192.168.127.1 metric 1 2>/dev/null; "
            "ls /sys/class/net | grep -v lo | tr '\\n' ' '; echo; "
            "curl -s -m 8 -o /dev/null -w '%{http_code}' http://1.1.1.1/ ; echo RC=$?",
        )
        # Even as root and after fiddling, the only path off-silicon is our
        # softnet, so the internet stays blocked.
        assert "RC=0" not in esc.stdout, f"root guest escaped: {esc.stdout!r}"
    finally:
        _cleanup(name, proc, softnet_bin)
        logf.close()
        registry.remove("sandbox", name)


def _wait_marker(marker_path: Path, proc, bindir: Path, timeout=90) -> dict:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if marker_path.exists():
            try:
                return json.loads(marker_path.read_text())
            except json.JSONDecodeError:
                pass
        if proc.poll() is not None:
            log = (bindir / "softnet.log").read_text()
            raise AssertionError(f"tart exited early rc={proc.returncode}:\n{log}")
        time.sleep(1)
    raise AssertionError("softnet never wrote its marker (never execd?)")


def _gexec(name: str, script: str, timeout=45):
    # one retry on tart-guest-agent gRPC transport flakes
    for _ in range(2):
        r = subprocess.run(
            ["tart", "exec", name, "bash", "-c", script],
            capture_output=True, text=True, timeout=timeout,
        )
        if r.returncode == 0 or "Transport became inactive" not in r.stderr:
            return r
        time.sleep(2)
    return r


def _wait_nic(name: str, timeout=60) -> list:
    deadline = time.monotonic() + timeout
    nics = []
    while time.monotonic() < deadline:
        r = _gexec(name, "ls /sys/class/net | grep -v lo | sort")
        nics = [n for n in r.stdout.split() if n]
        if nics:
            return nics
        time.sleep(2)
    return nics


def _wait_ip(name: str, timeout=90) -> str:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        r = _gexec(
            name,
            "ip -4 -o addr show scope global | awk '{print $4}' | cut -d/ -f1",
        )
        ip = r.stdout.strip().splitlines()[0].strip() if r.stdout.strip() else ""
        if ip:
            return ip
        time.sleep(2)
    return ""


def _cleanup(name: str, proc, softnet_bin: Path):
    # `tart stop` HANGS on a softnet-backed VM (no graceful shutdown path), and
    # the softnet child can outlive tart. Kill the run process, pkill softnet by
    # its unique temp path, then delete.
    if proc is not None:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
    subprocess.run(["pkill", "-9", "-f", str(softnet_bin)], capture_output=True)
    for _ in range(10):
        if _state(name) in ("stopped", "absent"):
            break
        time.sleep(1)
    subprocess.run(["tart", "delete", name], capture_output=True, timeout=15)
