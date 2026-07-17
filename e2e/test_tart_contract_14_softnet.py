"""Tart-contract e2e: prove the whole tart -> userspace-softnet -> gvisor loop,
end to end, on a real VM, as the UNPRIVILEGED user (no sudo, no SUID).

This is NOT a test of devm's production code — it drives `tart` directly and
builds its own throwaway `softnet` binary (from ../e2e/contract/softnet, an isolated Go
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
import signal
import socket
import socketserver
import subprocess
import threading
import time
from pathlib import Path

import pytest

from helpers import registry

FIXTURE_DIR = Path(__file__).parent / "contract" / "softnet"
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
            ["mktemp", "-d", "-t", "softnet-fixture-bin"],
            capture_output=True, text=True, check=True,
        ).stdout.strip()
    )
    softnet_bin = bindir / "softnet"
    build = subprocess.run(
        ["go", "build", "-o", str(softnet_bin), "."],
        cwd=FIXTURE_DIR, capture_output=True, text=True, timeout=180,
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
    env["FIXTURE_ALLOW"] = f"127.0.0.1:{allow_port}"  # post-NAT dial target
    env["FIXTURE_MARKER"] = str(marker_path)

    name = f"e2e-contract-softnet-{secrets.token_hex(2)}"
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
        assert mk["fixture_allow"] == [f"127.0.0.1:{allow_port}"]

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

        # ALLOWED via our gvisor DNS (allowed.fixture.test -> NAT alias)
        via_dns = _gexec(
            name,
            f"curl -s -m 8 -o /dev/null -w '%{{http_code}}' http://allowed.fixture.test:{allow_port}/",
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


def _softnet_pid(softnet_bin: Path) -> int:
    r = subprocess.run(
        ["pgrep", "-f", str(softnet_bin)], capture_output=True, text=True
    )
    pids = [int(p) for p in r.stdout.split() if p.strip()]
    return pids[0] if pids else 0


def _wait_proc_exit(proc, timeout=20) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if proc.poll() is not None:
            return True
        time.sleep(0.5)
    return False


@pytest.mark.contract
def test_tart_contract_softnet_child_death_and_restart_recovery():
    """Contract canary: tart treats its softnet child as LOAD-BEARING, and a
    fresh `tart run` brings softnet back up.

    Direction 1 (child death): if softnet dies mid-VM, `tart run` exits and the
    VM stops (observed on tart 2.32.1: RuntimeFailed, "Softnet process
    terminated prematurely"). devm's crash-recovery design depends on this: the
    daemon supervises `tart run`, NOT softnet, so a softnet crash must surface
    as a tart-run exit for the existing restart-with-backoff to kick in.

    Direction 2 (restart recovery): re-running `tart run --net-softnet` on the
    same VM re-execs a fresh softnet (spawning softnet is part of --net-softnet
    startup) and the VM reaches running again — proving the disk survives the
    hard stop and the supervisor's restart genuinely restores networking.

    If a future tart version tolerated a dead child, auto-restarted it, or
    stopped exec'ing softnet on startup, this assumption breaks silently and
    we'd need an explicit softnet health path — this test is the canary.
    """
    bindir = Path(
        subprocess.run(
            ["mktemp", "-d", "-t", "softnet-death-bin"],
            capture_output=True, text=True, check=True,
        ).stdout.strip()
    )
    softnet_bin = bindir / "softnet"
    build = subprocess.run(
        ["go", "build", "-o", str(softnet_bin), "."],
        cwd=FIXTURE_DIR, capture_output=True, text=True, timeout=180,
    )
    assert build.returncode == 0, f"go build softnet failed:\n{build.stderr}"

    marker_path = bindir / "marker.json"
    env = dict(os.environ)
    env["PATH"] = str(bindir) + os.pathsep + env["PATH"]
    env["FIXTURE_ALLOW"] = "127.0.0.1:59999"  # unused; softnet only dials on demand
    env["FIXTURE_MARKER"] = str(marker_path)

    name = f"e2e-contract-softnet-death-{secrets.token_hex(2)}"
    registry.append("sandbox", name)
    proc = None
    logf = open(bindir / "softnet.log", "w")
    try:
        _tart("clone", TEMPLATE, name, timeout=90)
        proc = subprocess.Popen(
            ["tart", "run", "--no-graphics", "--net-softnet", name],
            stdout=logf, stderr=subprocess.STDOUT, env=env,
        )
        # softnet is up once it writes its contract marker
        _wait_marker(marker_path, proc, bindir)

        # let the VM reach running so the virtio NIC attachment is live
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline and _state(name) != "running":
            time.sleep(2)
        assert _state(name) == "running", "VM never reached running"

        sn_pid = _softnet_pid(softnet_bin)
        assert sn_pid, "softnet child process not found"
        assert proc.poll() is None, "tart run exited before we killed softnet"

        # --- the contract: kill softnet, tart run MUST exit and the VM MUST stop ---
        os.kill(sn_pid, signal.SIGKILL)
        assert _wait_proc_exit(proc, timeout=20), (
            "tart run did NOT exit after its softnet child died — the "
            "crash-recovery assumption (supervisor watching tart run) is broken"
        )
        st = _state(name)
        assert st in ("stopped", "absent"), f"VM state after softnet death: {st}"

        # --- direction 2: a fresh `tart run` re-execs softnet and recovers ---
        marker_path.unlink(missing_ok=True)  # so we detect the NEW softnet
        proc = subprocess.Popen(
            ["tart", "run", "--no-graphics", "--net-softnet", name],
            stdout=logf, stderr=subprocess.STDOUT, env=env,
        )
        mk2 = _wait_marker(marker_path, proc, bindir)  # fresh softnet came up
        assert mk2["euid"] != 0, "restarted softnet not running as expected"
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline and _state(name) != "running":
            time.sleep(2)
        assert _state(name) == "running", "VM did not recover to running after restart"
    finally:
        _cleanup(name, proc, softnet_bin)
        logf.close()
        registry.remove("sandbox", name)


def _pingpong(host: str, port: int, nonce: str, timeout=8, retries=20) -> str:
    """Connect to host:port, send nonce, return the echoed reply. Retries to
    absorb the listener / guest-echo / ARP startup races."""
    payload = (nonce + "\n").encode()
    for _ in range(retries):
        try:
            with socket.create_connection((host, port), timeout=timeout) as s:
                s.sendall(payload)
                s.settimeout(timeout)
                data = b""
                while b"\n" not in data and len(data) < 4096:
                    chunk = s.recv(1024)
                    if not chunk:
                        break
                    data += chunk
                got = data.decode(errors="replace").strip()
                if got == nonce:
                    return got
        except OSError:
            pass
        time.sleep(1)
    return ""


@pytest.mark.contract
def test_tart_contract_softnet_ingress_pingpong():
    """Contract: softnet carries host->guest INGRESS through the userspace
    netstack (the expose / port-forward direction).

    Under softnet the guest IP (192.168.127.x) is NOT host-routable, so every
    host->guest path — direct services (e.g. Postgres), the Caddy/.test HTTP
    backend dial, and SSH (`devm shell`) — reaches the guest ONLY via softnet's
    host-side listener injecting into the netstack. This proves that path: a
    guest-side socat echo server round-trips a nonce sent from the Mac through
    softnet's expose listener. If gvisor-tap-vsock's host->guest dial ever
    regressed, ingress (including `devm shell` over SSH) would break with it.
    """
    bindir = Path(
        subprocess.run(
            ["mktemp", "-d", "-t", "softnet-ingress-bin"],
            capture_output=True, text=True, check=True,
        ).stdout.strip()
    )
    softnet_bin = bindir / "softnet"
    build = subprocess.run(
        ["go", "build", "-o", str(softnet_bin), "."],
        cwd=FIXTURE_DIR, capture_output=True, text=True, timeout=180,
    )
    assert build.returncode == 0, f"go build softnet failed:\n{build.stderr}"

    marker_path = bindir / "marker.json"
    host_port = _free_port()  # softnet's host-side ingress listener
    guest_port = 15432        # guest echo server (direct-service-like)
    env = dict(os.environ)
    env["PATH"] = str(bindir) + os.pathsep + env["PATH"]
    env["FIXTURE_ALLOW"] = "127.0.0.1:1"  # unused in this test
    env["FIXTURE_MARKER"] = str(marker_path)
    env["FIXTURE_EXPOSE"] = f"{host_port}:{guest_port}"

    name = f"e2e-contract-softnet-ingress-{secrets.token_hex(2)}"
    registry.append("sandbox", name)
    proc = None
    logf = open(bindir / "softnet.log", "w")
    try:
        _tart("clone", TEMPLATE, name, timeout=90)
        proc = subprocess.Popen(
            ["tart", "run", "--no-graphics", "--net-softnet", name],
            stdout=logf, stderr=subprocess.STDOUT, env=env,
        )
        _wait_marker(marker_path, proc, bindir)

        deadline = time.monotonic() + 120
        while time.monotonic() < deadline and _state(name) != "running":
            time.sleep(2)
        assert _state(name) == "running", "VM never reached running"

        ip = _wait_ip(name)
        assert ip.startswith(SOFTNET_SUBNET_PREFIX), f"guest IP {ip} not from our DHCP"

        # guest-side echo server (socat ships in cirruslabs/debian)
        launch = _gexec(
            name,
            f"nohup socat TCP-LISTEN:{guest_port},fork,reuseaddr EXEC:/bin/cat "
            ">/dev/null 2>&1 & echo LAUNCHED",
        )
        assert "LAUNCHED" in launch.stdout, f"echo server launch: {launch.stdout!r}"

        # from the Mac, round-trip a nonce THROUGH softnet's host listener
        nonce = secrets.token_hex(8)
        got = _pingpong("127.0.0.1", host_port, nonce)
        assert got == nonce, f"ingress ping-pong failed: sent {nonce!r} got {got!r}"
    finally:
        _cleanup(name, proc, softnet_bin)
        logf.close()
        registry.remove("sandbox", name)


def _serve_udp(port: int) -> socket.socket:
    """Host-side UDP echo — stands in for devm's host NTP responder."""
    sock = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
    sock.bind(("127.0.0.1", port))

    def loop():
        while True:
            try:
                data, addr = sock.recvfrom(2048)
                sock.sendto(data, addr)
            except OSError:
                return

    threading.Thread(target=loop, daemon=True).start()
    return sock


@pytest.mark.contract
def test_tart_contract_softnet_udp_ntp_forward():
    """Contract: softnet forwards guest outbound UDP (the NTP :123 path) to a
    host endpoint AND keeps owning DNS at the same time.

    Under softnet the guest can't reach external NTP (egress is caged), so
    post-Mac-sleep clock-drift heal relies on devm's host NTP responder — the
    guest's timesyncd hits udp:123, which today the funnel DNATs to the
    responder. This proves the softnet equivalent: a guest UDP datagram to :123
    (any dest IP, mirroring the dport-based DNAT) reaches a host UDP listener
    and the reply returns, WHILE softnet's own resolver still answers *.test.
    If gvisor UDP egress forwarding regressed, post-sleep clock heal (hence TLS)
    would break.
    """
    bindir = Path(
        subprocess.run(
            ["mktemp", "-d", "-t", "softnet-udp-bin"],
            capture_output=True, text=True, check=True,
        ).stdout.strip()
    )
    softnet_bin = bindir / "softnet"
    build = subprocess.run(
        ["go", "build", "-o", str(softnet_bin), "."],
        cwd=FIXTURE_DIR, capture_output=True, text=True, timeout=180,
    )
    assert build.returncode == 0, f"go build softnet failed:\n{build.stderr}"

    marker_path = bindir / "marker.json"
    echo_port = _free_port()  # host UDP echo (stands in for the NTP responder)
    _serve_udp(echo_port)
    env = dict(os.environ)
    env["PATH"] = str(bindir) + os.pathsep + env["PATH"]
    env["FIXTURE_ALLOW"] = "127.0.0.1:1"  # unused here
    env["FIXTURE_MARKER"] = str(marker_path)
    env["FIXTURE_UDP_FWD"] = f"123:127.0.0.1:{echo_port}"

    name = f"e2e-contract-softnet-udp-{secrets.token_hex(2)}"
    registry.append("sandbox", name)
    proc = None
    logf = open(bindir / "softnet.log", "w")
    try:
        _tart("clone", TEMPLATE, name, timeout=90)
        proc = subprocess.Popen(
            ["tart", "run", "--no-graphics", "--net-softnet", name],
            stdout=logf, stderr=subprocess.STDOUT, env=env,
        )
        _wait_marker(marker_path, proc, bindir)

        deadline = time.monotonic() + 120
        while time.monotonic() < deadline and _state(name) != "running":
            time.sleep(2)
        assert _state(name) == "running", "VM never reached running"

        ip = _wait_ip(name)
        assert ip.startswith(SOFTNET_SUBNET_PREFIX), f"guest IP {ip} not from our DHCP"

        # NTP-shaped UDP round-trip: guest -> :123 (arbitrary dest IP, caught by
        # dport like the funnel) -> host echo -> reply back. python3 ships in the
        # image and is deterministic for a UDP request/reply.
        nonce = secrets.token_hex(8)
        py = (
            "python3 -c \"import socket;"
            "s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.settimeout(4);"
            f"s.sendto(b'{nonce}',('1.2.3.4',123));"
            "print(s.recvfrom(2048)[0].decode())\""
        )
        last = None
        for _ in range(4):
            last = _gexec(name, py)
            if nonce in last.stdout:
                break
            time.sleep(1)
        assert nonce in last.stdout, f"udp :123 round-trip failed: {last.stdout!r} / {last.stderr!r}"

        # DNS is still owned by softnet alongside the UDP forwarder (coexistence).
        dns = _gexec(name, "getent hosts allowed.fixture.test | awk '{print $1}'")
        assert "192.168.127.254" in dns.stdout, f"softnet DNS broke under UDP fwd: {dns.stdout!r}"
    finally:
        _cleanup(name, proc, softnet_bin)
        logf.close()
        registry.remove("sandbox", name)
