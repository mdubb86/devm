"""111: `direct: true` live add + live withdraw via `devm reconcile` —
no shell, no teardown, no VM bounce.

Modeled on test_92_iron_proxy_reconcile.py (edit devm.yaml → `devm
reconcile` → assert new behavior live, on the SAME running VM,
tart-PID-unchanged bounce proof) crossed with test_37_route_vm.py's
`/routes` probe, and reuses test_91_docker.py's docker-in-VM scaffold.
The design doc's `KindServiceDirectChange` bucket is `BucketLive` with
a real `ApplyLive` path (NOT the "classified live but silently
deferred to next cold-start" trap `KindPortAdd` falls into) — this
test is the proof that flipping `direct` takes effect on the spot.

A real CONTAINER is required (not a host process): only a
container-published port traverses the `forward` hook that
`svc_ingress` guards. A tiny `busybox nc` listener (see test_110) is
started ONCE, published on the service's declared port, and left
running for the whole test — the thing that changes across reconciles
is purely the firewall/DNS/route state around it, isolating the
assertions to the live-apply path itself.

Sequence:
  1. `devm start` with `docker: true` + a service that has a hostname
     but `direct: false` (proxied — the default). Baseline: DNS
     answers 127.0.0.1, no `svc_ingress` rule for the port.
  2. Start the `nc` listener container, published at the declared port.
  3. Flip `direct: true`, `devm reconcile` (no shell). Assert: DNS
     now answers the VM IP; `svc_ingress` carries the accept; the
     port's banner is readable from the Mac.
  4. Flip back to `direct: false`, `devm reconcile`. Assert: DNS
     reverts to 127.0.0.1; the `svc_ingress` rule is gone; the port is
     NO LONGER reachable from the Mac (forward hook's default-drop
     re-applies once the accept is withdrawn).

Throughout, the VM's `tart run` PID is checked unchanged (mirrors
test_92's bounce-proof) — direct changes must never recreate the VM.

What this pins:
  - Live add: `svc_ingress` flush-rebuild + route re-push + DNS answer
    change, all without `devm shell`/teardown.
  - Live withdraw: the SAME machinery in reverse — DNS reverts, the
    accept rule is gone, and (crucially) the port actually stops being
    reachable, not just that the config LOOKS reverted.
  - No VM bounce for either direction.

What it doesn't cover (tested elsewhere):
  - Cold-start-time correctness (routes/nft/Caddyfile/split-horizon
    freshly provisioned) — test_110.
  - Persistence across `devm stop`/reboot and the docker-vs-host-process
    firewall gate — test_112.
  - `direct: true` without hostname validation — test_113.

KNOWN GAP #1 (DNS): identical to test_110 — the Mac-side DNS
assertions self-skip when `$DEVM_DNS_ADDR` is the isolated lane's
ephemeral `127.0.0.1:0` (no picked-port accessor exists for the DNS
server). See test_110's module docstring.

KNOWN GAP #2 (reconcile stdout): `internal/orchestrator/format.go`'s
`formatChange`/`changeKindJSON` switches enumerate every other
`reconcile.Kind*` constant but have NO case for
`KindServiceDirectChange` — a direct flip currently renders as the
literal string `"(unknown change)"` in `devm reconcile` output (text
mode) and `"unknown"` (JSON mode), even though the underlying
live-apply (this test's real subject) works. Filed as a follow-up;
this test does not assert on `reconcile` stdout text for that reason —
only on functional behavior (DNS/nft/reachability).
"""
from __future__ import annotations

import http.client
import json
import os
import socket as _socket_module
import subprocess
import time

import pytest
import yaml

from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm

_SOCKET_PATH = os.path.join(
    os.environ.get(
        "DEVM_RUNTIME_DIR",
        os.path.expanduser("~/Library/Application Support/devm"),
    ),
    "devm.sock",
)

# Distinct from test_110's ports to avoid any port bleed if tests
# happen to run concurrently against distinct VMs.
DIRECT_PORT = 54422
CONTAINER_PORT = 9100
BANNER = b"devm-direct-e2e"


class _UnixSocketHTTP(http.client.HTTPConnection):
    def __init__(self, socket_path: str):
        super().__init__("localhost")
        self._socket_path = socket_path

    def connect(self) -> None:
        self.sock = _socket_module.socket(
            _socket_module.AF_UNIX, _socket_module.SOCK_STREAM
        )
        self.sock.connect(self._socket_path)


def _get_routes() -> dict[str, list]:
    conn = _UnixSocketHTTP(_SOCKET_PATH)
    conn.request("GET", "/routes")
    resp = conn.getresponse()
    assert resp.status == 200, f"GET /routes returned {resp.status}"
    return json.loads(resp.read())


def _dns_addr() -> tuple[str, int]:
    raw = os.environ.get("DEVM_DNS_ADDR", "127.0.0.1:51153")
    host, _, port_s = raw.rpartition(":")
    return (host or "127.0.0.1"), int(port_s)


def _dig_a(hostname: str, dns_host: str, dns_port: int, timeout: float = 5.0) -> str:
    r = subprocess.run(
        ["dig", "+short", "+time=2", "+tries=1",
         f"@{dns_host}", "-p", str(dns_port), hostname, "A"],
        capture_output=True, timeout=timeout,
    )
    if r.returncode != 0:
        return ""
    lines = [ln.strip() for ln in r.stdout.decode().splitlines() if ln.strip()]
    return lines[0] if lines else ""


def _tcp_connect(host: str, port: int, timeout: float = 3.0) -> bool:
    try:
        with _socket_module.create_connection((host, port), timeout=timeout):
            return True
    except OSError:
        return False


def _tcp_read_banner(host: str, port: int, expect: bytes, timeout: float = 3.0) -> bytes | None:
    try:
        with _socket_module.create_connection((host, port), timeout=timeout) as s:
            s.settimeout(timeout)
            return s.recv(len(expect))
    except OSError:
        return None


def _tart_pid(vm_name: str) -> int | None:
    out = subprocess.run(
        ["pgrep", "-f", f"tart run.*{vm_name}"],
        capture_output=True, text=True,
    )
    if out.returncode != 0:
        return None
    pids = [line for line in out.stdout.strip().splitlines() if line]
    return int(pids[0]) if pids else None


def _vm_ip(vm_name: str) -> str:
    r = subprocess.run(["tart", "ip", vm_name], capture_output=True, timeout=15)
    return r.stdout.decode().strip() if r.returncode == 0 else ""


def _svc_ingress(devm) -> str:
    r = devm_exec_with_retry(
        devm.path,
        ["sudo", "-n", "nft", "list", "chain", "inet", "devm_filter", "svc_ingress"],
        cwd=devm.cwd, timeout=30,
    )
    return r.stdout.decode() if r.returncode == 0 else ""


def _set_direct(workspace, value: bool) -> None:
    cfg = yaml.safe_load(workspace.devmyaml_path.read_text())
    cfg["services"]["nc"]["direct"] = value
    workspace.devmyaml_path.write_text(yaml.safe_dump(cfg, sort_keys=False))


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_direct_live_add_and_withdraw(workspace, devm, sandbox_name):
    hostname = f"{sandbox_name}-nc.test"
    workspace.write_devmyaml(
        docker=True,
        services={
            "nc": {"port": DIRECT_PORT, "hostname": hostname, "direct": False},
        },
    )

    start = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=480,
    )
    assert start.returncode == 0, (
        f"devm start failed:\nstderr={start.stderr.decode()!r}"
    )

    project_id = workspace.slug
    vm_ip = _vm_ip(workspace.vm_name)
    assert vm_ip, "could not get VM IP via `tart ip`"
    pid_before = _tart_pid(workspace.vm_name)
    assert pid_before is not None, "expected a running tart process for the VM"

    dns_host, dns_port = _dns_addr()
    dns_testable = dns_port != 0

    # ---- Baseline (direct: false): DNS answers loopback; no
    # ---- svc_ingress rule for this port yet. ----
    if dns_testable:
        baseline = _dig_a(hostname, dns_host, dns_port)
        assert baseline == "127.0.0.1", (
            f"non-direct hostname {hostname!r} should resolve to "
            f"127.0.0.1 before any direct flip; got {baseline!r}"
        )
    assert f"proto-dst {DIRECT_PORT}" not in _svc_ingress(devm), (
        "svc_ingress should have no rule for this port before any "
        "direct flip"
    )

    # ---- Bring up the `nc` listener container ONCE, published at the
    # ---- declared port. Stays up for the rest of the test — only the
    # ---- firewall/DNS/route state around it changes. ----
    run = devm_exec_with_retry(
        devm.path,
        ["docker", "run", "-d", "--rm", "--name", "e2e-direct-live-nc",
         "-p", f"{DIRECT_PORT}:{CONTAINER_PORT}",
         "busybox", "sh", "-c",
         f"while true; do printf '%s' '{BANNER.decode()}' | "
         f"nc -l -p {CONTAINER_PORT}; done"],
        cwd=str(workspace.path), timeout=120,
    )
    assert run.returncode == 0, (
        f"docker run busybox nc failed: rc={run.returncode}\n"
        f"stderr={run.stderr.decode()!r}"
    )

    try:
        # ================= LIVE ADD =================
        _set_direct(workspace, True)
        reconcile = devm.reconcile(yes=True, timeout=120)
        assert reconcile.returncode == 0, (
            f"devm reconcile (flip to direct) failed:\n"
            f"stdout={reconcile.stdout.decode()!r}\n"
            f"stderr={reconcile.stderr.decode()!r}"
        )

        routes = _get_routes()
        entry = next(
            (e for e in routes.get(project_id, []) if e["hostname"] == hostname),
            None,
        )
        assert entry is not None and entry.get("direct") is True, (
            f"route for {hostname!r} not marked direct after reconcile: "
            f"{routes.get(project_id)}"
        )

        if dns_testable:
            answer = _dig_a(hostname, dns_host, dns_port)
            assert answer == vm_ip, (
                f"after live direct-add, DNS should answer VM IP "
                f"{vm_ip!r} for {hostname!r}; got {answer!r}"
            )

        deadline = time.time() + 30
        nft_out = ""
        while time.time() < deadline:
            nft_out = _svc_ingress(devm)
            if f"proto-dst {DIRECT_PORT}" in nft_out:
                break
            time.sleep(1)
        assert f"ct original proto-dst {DIRECT_PORT} accept" in nft_out, (
            f"svc_ingress missing accept for port {DIRECT_PORT} after "
            f"live direct-add:\n{nft_out}"
        )

        deadline = time.time() + 30
        got = None
        while time.time() < deadline:
            got = _tcp_read_banner(vm_ip, DIRECT_PORT, BANNER, timeout=3)
            if got == BANNER:
                break
            time.sleep(1)
        assert got == BANNER, (
            f"Mac could not read the expected banner from "
            f"{vm_ip}:{DIRECT_PORT} after live direct-add (got {got!r}) "
            f"— expected reachable WITHOUT a shell/teardown"
        )

        assert _tart_pid(workspace.vm_name) == pid_before, (
            "VM was bounced by a live direct-add reconcile; it must "
            "apply live only"
        )

        # ================= LIVE WITHDRAW =================
        _set_direct(workspace, False)
        reconcile = devm.reconcile(yes=True, timeout=120)
        assert reconcile.returncode == 0, (
            f"devm reconcile (flip back) failed:\n"
            f"stdout={reconcile.stdout.decode()!r}\n"
            f"stderr={reconcile.stderr.decode()!r}"
        )

        if dns_testable:
            answer = _dig_a(hostname, dns_host, dns_port)
            assert answer == "127.0.0.1", (
                f"after live direct-withdraw, DNS should revert to "
                f"127.0.0.1 for {hostname!r}; got {answer!r}"
            )

        deadline = time.time() + 30
        nft_out = _svc_ingress(devm)
        while (
            f"proto-dst {DIRECT_PORT}" in nft_out
            and time.time() < deadline
        ):
            time.sleep(1)
            nft_out = _svc_ingress(devm)
        assert f"proto-dst {DIRECT_PORT}" not in nft_out, (
            f"svc_ingress still carries a rule for port {DIRECT_PORT} "
            f"after live direct-withdraw:\n{nft_out}"
        )

        # Give the flush-rebuild a moment to actually close the port,
        # then confirm the Mac can no longer reach it (not just that
        # the rule text is gone).
        time.sleep(2)
        assert not _tcp_connect(vm_ip, DIRECT_PORT, timeout=3), (
            f"Mac could still reach {vm_ip}:{DIRECT_PORT} after live "
            f"direct-withdraw — svc_ingress accept should have been "
            f"withdrawn, restoring the forward hook's default drop"
        )

        assert _tart_pid(workspace.vm_name) == pid_before, (
            "VM was bounced by a live direct-withdraw reconcile; it "
            "must apply live only"
        )
    finally:
        subprocess.run(
            [devm.path, "exec", "docker", "rm", "-f", "e2e-direct-live-nc"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
