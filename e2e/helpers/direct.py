"""Shared probes for the direct-service e2e tests (test_110/111/112).

Extracted from the per-file copies so the routes / DNS / TCP-reachability /
svc_ingress probes live in one place. See each test module's docstring for
what it pins.

DNS note: `dns_addr()` honors `$DEVM_DNS_ADDR`. The isolated e2e lane sets it
to `127.0.0.1:0` (ephemeral) and exposes no picked-port accessor, so callers
treat port 0 as "not queryable" and soft-skip the Mac-side DNS sub-assertions.
"""
from __future__ import annotations

import http.client
import json
import os
import socket as _socket
import subprocess
import time

from helpers.exec_retry import devm_exec_with_retry

# The banner the netcat listeners emit on connect — reading it back proves
# real data flow through the whole path, not just a bare SYN/ACK.
BANNER = b"devm-direct-e2e"


def socket_path() -> str:
    """Daemon Unix socket, honoring the isolated lane's DEVM_RUNTIME_DIR
    (falls back to the installed daemon's default). See test_37_route_vm.py."""
    return os.path.join(
        os.environ.get(
            "DEVM_RUNTIME_DIR",
            os.path.expanduser("~/Library/Application Support/devm"),
        ),
        "devm.sock",
    )


class UnixSocketHTTP(http.client.HTTPConnection):
    """HTTPConnection over a Unix domain socket."""

    def __init__(self, path: str):
        super().__init__("localhost")
        self._path = path

    def connect(self) -> None:
        self.sock = _socket.socket(_socket.AF_UNIX, _socket.SOCK_STREAM)
        self.sock.connect(self._path)


def get_routes() -> dict[str, list]:
    """GET /routes from the daemon; returns project_id -> [route, ...]."""
    conn = UnixSocketHTTP(socket_path())
    conn.request("GET", "/routes")
    resp = conn.getresponse()
    assert resp.status == 200, f"GET /routes returned {resp.status}"
    return json.loads(resp.read())


def ingress_config(project_id: str) -> dict:
    """GET /vm/ingress-config?name=<project_id> from the daemon; returns
    the decoded body (e.g. {"ssh_host_port": N})."""
    conn = UnixSocketHTTP(socket_path())
    conn.request("GET", f"/vm/ingress-config?name={project_id}")
    resp = conn.getresponse()
    body = resp.read()
    assert resp.status == 200, f"GET /vm/ingress-config returned {resp.status}: {body!r}"
    return json.loads(body)


def dns_addr() -> tuple[str, int]:
    """Host/port of the daemon's *.test resolver (internal/serviceapi/dns.go
    DNSAddr()). Port 0 means the isolated lane's ephemeral bind — not
    queryable (see module docstring)."""
    raw = os.environ.get("DEVM_DNS_ADDR", "127.0.0.1:51153")
    host, _, port_s = raw.rpartition(":")
    return (host or "127.0.0.1"), int(port_s)


def dig_a(hostname: str, dns_host: str, dns_port: int, timeout: float = 5.0) -> str:
    """First A-record answer for hostname from dns_host:dns_port, or ''."""
    r = subprocess.run(
        ["dig", "+short", "+time=2", "+tries=1",
         f"@{dns_host}", "-p", str(dns_port), hostname, "A"],
        capture_output=True, timeout=timeout,
    )
    if r.returncode != 0:
        return ""
    lines = [ln.strip() for ln in r.stdout.decode().splitlines() if ln.strip()]
    return lines[0] if lines else ""


def tcp_connect(host: str, port: int, timeout: float = 3.0) -> bool:
    try:
        with _socket.create_connection((host, port), timeout=timeout):
            return True
    except OSError:
        return False


def tcp_read_banner(host: str, port: int, expect: bytes, timeout: float = 3.0) -> bytes | None:
    """Connect and read len(expect) bytes; returns what was read, or None on
    any connection error. Caller compares to `expect`."""
    try:
        with _socket.create_connection((host, port), timeout=timeout) as s:
            s.settimeout(timeout)
            return s.recv(len(expect))
    except OSError:
        return None


def wait_reachable(host: str, port: int, timeout: float = 40.0) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if tcp_connect(host, port, timeout=3):
            return True
        time.sleep(1)
    return False


def vm_ip(vm_name: str) -> str:
    r = subprocess.run(["tart", "ip", vm_name], capture_output=True, timeout=15)
    return r.stdout.decode().strip() if r.returncode == 0 else ""


def svc_ingress(devm) -> str:
    """`nft list chain inet devm_filter svc_ingress` inside the VM, or '' if
    the chain doesn't exist / the exec failed."""
    r = devm_exec_with_retry(
        devm.path,
        ["sudo", "-n", "nft", "list", "chain", "inet", "devm_filter", "svc_ingress"],
        cwd=devm.cwd, timeout=30,
    )
    return r.stdout.decode() if r.returncode == 0 else ""
