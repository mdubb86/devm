"""Contract e2e: the PRODUCTION `devm` binary, exec'd as `softnet` (via a
symlink, exactly as tart resolves `--net-softnet` on $PATH), driven purely
through the control-socket protocol.

Unlike e2e/test_tart_contract_14_softnet.py (which builds a throwaway fixture
binary from an isolated Go module to prove tart<->gvisor-tap-vsock feasibility
root-free), this test builds `./cmd/devm` itself — internal/softnet — and
proves the real three-state egress state machine (LOCKED/OPEN/ENFORCED) is
reachable end to end on a real VM:

  tart run --net-softnet
    -> execs the `softnet`-named symlink to our production binary
    -> main() dispatches to softnet.Run() (argv[0] == "softnet")
    -> softnet boots its gvisor netstack, DNS, and a unix-socket control
       server at $SOFTNET_CONTROL_SOCK
    -> a control client (this test) sends newline-delimited JSON ControlMsgs
       ({"op":"setPolicy",...}) to flip egress policy live
    -> the guest's outbound TCP is allowed/denied/forwarded per the current
       policy, with no restart of softnet required

No JSON marker exists in production softnet (that was fixture-only
instrumentation) — readiness is inferred from tart state == running plus a
DHCP-leased 192.168.127.x guest IP, which only our own netstack hands out.

Plain cirruslabs/debian clone (NOT devm-base): devm-base's default-drop
nftables egress lock is scoped to the vmnet gateway and would sabotage egress
under softnet's distinct 192.168.127.0/24 gateway for reasons unrelated to
the mechanism under test — same rationale as the fixture contract test.
"""
import json
import os
import secrets
import socket
import subprocess
import time
from pathlib import Path

import pytest

from helpers import registry
from test_tart_contract_14_softnet import (
    _free_port,
    _serve,
    _tart,
    _state,
    _wait_ip,
    _gexec,
)

REPO_ROOT = Path(__file__).parent.parent
TEMPLATE = "ghcr.io/cirruslabs/debian:latest"
NAT_ALIAS = "192.168.127.254"
SOFTNET_SUBNET_PREFIX = "192.168.127."


def _control(sock_path: str, msg: dict) -> bool:
    for _ in range(30):
        try:
            with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as s:
                s.connect(sock_path)
                s.sendall((json.dumps(msg) + "\n").encode())
                return True
        except OSError:
            time.sleep(0.5)
    return False


def _curl_code(name: str, port: int, timeout=8) -> str:
    r = _gexec(
        name,
        f"curl -s -m {timeout} -o /dev/null -w '%{{http_code}}' "
        f"http://{NAT_ALIAS}:{port}/ ; echo RC=$?",
    )
    return r.stdout


def _cleanup(name: str, proc, softnet_bin: Path):
    # `tart stop` HANGS on a softnet-backed VM. Kill the run process, pkill
    # the softnet child by its symlinked path (that's the argv[0]/exe path
    # tart execd), then delete.
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


@pytest.mark.contract
def test_softnet_production_egress_locked_then_enforced():
    # --- build the PRODUCTION binary and symlink it as `softnet` on $PATH ---
    bindir = Path(
        subprocess.run(
            ["mktemp", "-d", "-t", "softnet-prod-bin"],
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

    # --- stand up the iron-proxy stand-in on the host ---
    proxy_port = _free_port()
    _serve(proxy_port, b"ENFORCED-OK")
    proxy_addr = f"127.0.0.1:{proxy_port}"

    sock_path = str(bindir / "control.sock")
    env = dict(os.environ)
    env["PATH"] = str(bindir) + os.pathsep + env["PATH"]
    env["SOFTNET_CONTROL_SOCK"] = sock_path

    name = f"e2e-softnet-prod-{secrets.token_hex(2)}"
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
        assert _control(sock_path, {"op": "setPolicy", "policy": "LOCKED"}), (
            f"never connected to control socket {sock_path}; log:\n"
            f"{(bindir / 'softnet.log').read_text()}"
        )

        # --- LOCKED: all egress denied ---
        locked = None
        for _ in range(6):
            locked = _curl_code(name, proxy_port)
            if "RC=0" not in locked:
                break
            time.sleep(1)
        assert "RC=0" not in locked, f"LOCKED but egress succeeded: {locked!r}"

        # --- ENFORCED: :443 forwards to iron-proxy HTTPS endpoint, :5432 denied ---
        ok = _control(
            sock_path,
            {
                "op": "setPolicy",
                "policy": "ENFORCED",
                "iron_proxy": {
                    "http": proxy_addr,
                    "https": proxy_addr,
                    "dns": proxy_addr,
                    "ntp": proxy_addr,
                },
            },
        )
        assert ok, "failed to send ENFORCED setPolicy"

        allowed = None
        for _ in range(8):
            r = _gexec(
                name,
                f"curl -s -m 8 -o /dev/null -w '%{{http_code}}' http://{NAT_ALIAS}:443/",
            )
            allowed = r.stdout.strip()
            if allowed == "200":
                break
            time.sleep(1)
        assert allowed == "200", f"ENFORCED :443 curl: {allowed!r}"

        blocked = None
        for _ in range(6):
            blocked = _curl_code(name, 5432)
            if "RC=0" not in blocked:
                break
            time.sleep(1)
        assert "RC=0" not in blocked, f"ENFORCED :5432 (non-80/443) reachable: {blocked!r}"
    finally:
        _cleanup(name, proc, softnet_bin)
        logf.close()
        registry.remove("sandbox", name)
