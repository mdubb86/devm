"""Daemon-driven softnet ingress e2e (Plan 4, Task 4).

End-to-end proof that softnet INGRESS works through the real daemon
while EGRESS stays enforced. Companion to test_softnet_20_daemon_egress
(the egress-side proof of the same Plan 3/4 cutover) — this test drives
the daemon's ingress side: the softnet expose map computed from the
project config (Task 1), the per-project SSH host port (Task 2), and
the proxied-HTTP backend rewrite to the host-local softnet listener
(Task 3).

The project fixture declares:
  - a DIRECT raw-TCP service (`direct: true`): a `socat` TCP echo
    listener on the guest, reachable at 127.0.0.1:<port> on the Mac via
    softnet's host-side expose listener (no Caddy, no HTTP proxy — a
    straight splice).
  - a PROXIED HTTP service: a `caddy file-server` on the guest serving
    a known body, fronted (per Task 3) by a daemon route whose
    backend_host/backend_port point at the SAME host-local softnet
    listener mechanism as the direct service (softnet.HostLoopIP,
    svc.Port) — the outer daemon :80/:443 Host-header hop (via launchd
    socket activation, internal/serviceapi/runner.go sockact.Activate)
    is a separate concern from this test (same boundary
    test_37_route_vm.py draws) — what THIS test proves is the backend
    dial target Task 3 rewired: GET /routes shows the softnet-listener
    backend, and an HTTP GET at that exact address returns the guest
    server's body.
  - relies on the auto-exposed SSH port (Task 2): GET
    /vm/ingress-config, then a raw TCP connect for the SSH banner.

Egress must stay enforced throughout (assertion 4 mirrors
test_softnet_20's core check) — proving the new ingress listeners
didn't punch a hole in the softnet egress gate. Assertion 5 confirms
the old guest-nftables `svc_ingress` ingress path (Plan 3 Task 10) is
fully gone: no chain, no devm_* tables at all under softnet.
"""
from __future__ import annotations

import http.client
import secrets
import socket
import subprocess
import time

import pytest

from helpers.direct import dig_a as _dig_a, dns_addr as _dns_addr, get_routes as _get_routes, ingress_config as _ingress_config
from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


def _free_port() -> int:
    """An ephemeral free TCP port on the Mac side. Picked fresh per test
    run (not a fixed literal) so a leftover/orphaned softnet listener
    from a prior run — `tart delete` doesn't always reap the softnet
    child process promptly, a pre-existing environment reality — can't
    silently squat the same host port and starve this run's expose-map
    bind (see helpers/iron_proxy.py's identical pattern)."""
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _echo_roundtrip(host: str, port: int, payload: bytes, timeout: float = 3.0) -> bytes | None:
    try:
        with socket.create_connection((host, port), timeout=timeout) as s:
            s.sendall(payload)
            s.settimeout(timeout)
            return s.recv(len(payload))
    except OSError:
        return None


def _http_get(host: str, port: int, host_header: str, timeout: float = 3.0) -> tuple[int, bytes] | None:
    conn = http.client.HTTPConnection(host, port, timeout=timeout)
    try:
        conn.request("GET", "/", headers={"Host": host_header})
        resp = conn.getresponse()
        return resp.status, resp.read()
    except OSError:
        return None
    finally:
        conn.close()


def _tcp_banner(host: str, port: int, nbytes: int = 64, timeout: float = 3.0) -> bytes | None:
    try:
        with socket.create_connection((host, port), timeout=timeout) as s:
            s.settimeout(timeout)
            return s.recv(nbytes)
    except OSError:
        return None


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_daemon_softnet_ingress(workspace, devm, sandbox_name):
    direct_hostname = f"{sandbox_name}-echo.test"
    proxy_hostname = f"{sandbox_name}-web.test"
    web_body = f"devm-ingress-proxied-{secrets.token_hex(8)}"
    direct_port = _free_port()
    proxy_port = _free_port()

    workspace.write_devmyaml(
        # socat and caddy both ship in devm-base already (provision-base.sh
        # installs caddy; socat is present via cirruslabs/debian's own base
        # package set) — no `packages:` apt-install stage needed here.
        install=[
            "mkdir -p /tmp/e2e-web",
            f"printf '%s' '{web_body}' > /tmp/e2e-web/index.html",
        ],
        services={
            "echo": {
                "port": direct_port,
                "hostname": direct_hostname,
                "direct": True,
                "exec": ["socat", f"TCP-LISTEN:{direct_port},fork,reuseaddr", "EXEC:/bin/cat"],
                "restart": "always",
            },
            "web": {
                "port": proxy_port,
                "hostname": proxy_hostname,
                "exec": ["caddy", "file-server", "--listen", f":{proxy_port}", "--root", "/tmp/e2e-web"],
                "restart": "always",
            },
        },
        network={"allow": ["api.github.com"]},
    )

    sandbox = TartSandbox(name=sandbox_name)
    project_id = workspace.slug

    # --- cold-start through the daemon (normal `devm shell` path — the
    # --- adopt-in-place raw-`tart run` path is a known, separately
    # --- tracked gap; not exercised here). ---
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=600,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"
    assert sandbox.state() == "running", (
        f"expected VM running after cold-start; got {sandbox.state()!r}"
    )

    # ================================================================
    # Assertion 1: DIRECT service — a Mac-side TCP connect to
    # 127.0.0.1:<direct_port> reaches the guest socat echo listener and
    # round-trips a payload. Retries absorb softnet-listener / package-
    # install / socat-startup races.
    # ================================================================
    nonce = secrets.token_hex(8).encode()
    deadline = time.time() + 90
    got = None
    while time.time() < deadline:
        got = _echo_roundtrip("127.0.0.1", direct_port, nonce)
        if got == nonce:
            break
        time.sleep(2)
    assert got == nonce, (
        f"direct service round-trip failed at 127.0.0.1:{direct_port}: "
        f"sent {nonce!r}, got {got!r} — softnet's expose listener never "
        f"reached the guest socat echo (check the softnet process log "
        f"for its expose-listener startup line, and confirm socat is "
        f"listening on guest:{direct_port} via `tart exec`)"
    )

    # DNS: `<direct_hostname>` resolves to 127.0.0.1 via the daemon's
    # *.test resolver (ingress is unified to host loopback under
    # softnet, direct or proxied — see internal/serviceapi/dns.go).
    dns_host, dns_port = _dns_addr()
    answer = _dig_a(direct_hostname, dns_host, dns_port)
    assert answer == "127.0.0.1", (
        f"expected {direct_hostname!r} to resolve to 127.0.0.1 via "
        f"the daemon's *.test resolver; got {answer!r}"
    )

    # ================================================================
    # Assertion 2: PROXIED service — GET /routes shows the softnet-
    # listener backend Task 3 wired up, and an HTTP GET at that exact
    # backend address returns the guest caddy file-server's body. (The
    # outer daemon :80/:443 Host-header hop needs launchd socket
    # activation, unavailable to isolated `devm serve --foreground` —
    # see module docstring.)
    # ================================================================
    deadline = time.time() + 60
    entry = None
    while time.time() < deadline:
        routes = _get_routes()
        entry = next(
            (e for e in routes.get(project_id, []) if e["hostname"] == proxy_hostname), None
        )
        if entry is not None:
            break
        time.sleep(2)
    assert entry is not None, (
        f"no /routes entry for {proxy_hostname!r} in project {project_id!r} "
        f"— `devm shell`'s auto route-apply (vm mode) never registered it"
    )
    assert not entry.get("direct"), f"proxied route wrongly marked direct: {entry}"
    assert entry.get("backend_host") == "127.0.0.1" and entry.get("backend_port") == proxy_port, (
        f"proxied route backend should be the host-local softnet "
        f"listener 127.0.0.1:{proxy_port} (Task 3's rewrite); got "
        f"backend_host={entry.get('backend_host')!r} "
        f"backend_port={entry.get('backend_port')!r}"
    )

    deadline = time.time() + 90
    result = None
    while time.time() < deadline:
        result = _http_get("127.0.0.1", proxy_port, proxy_hostname)
        if result is not None and result[0] == 200 and result[1] == web_body.encode():
            break
        time.sleep(2)
    assert result is not None, (
        f"HTTP GET to 127.0.0.1:{proxy_port} (the proxied route's "
        f"backend) never connected — softnet's expose listener for the "
        f"proxied service never came up"
    )
    status, body = result
    assert status == 200 and body == web_body.encode(), (
        f"proxied service response mismatch: status={status} "
        f"body={body!r} (expected 200 / {web_body.encode()!r})"
    )

    # ================================================================
    # Assertion 3: SSH — GET /vm/ingress-config returns a nonzero
    # ssh_host_port, and a Mac-side TCP connect to
    # 127.0.0.1:<ssh_host_port> gets an SSH banner.
    # ================================================================
    cfg = _ingress_config(project_id)
    ssh_port = cfg.get("ssh_host_port", 0)
    assert ssh_port != 0, f"GET /vm/ingress-config returned ssh_host_port=0: {cfg}"

    deadline = time.time() + 60
    banner = None
    while time.time() < deadline:
        banner = _tcp_banner("127.0.0.1", ssh_port)
        if banner and b"SSH-2.0" in banner:
            break
        time.sleep(2)
    assert banner and b"SSH-2.0" in banner, (
        f"no SSH banner from 127.0.0.1:{ssh_port} (got {banner!r}) — "
        f"softnet's expose forward to guest:22 isn't reaching sshd"
    )

    # ================================================================
    # Assertion 4: EGRESS still enforced — opening ingress listeners
    # must not have punched an egress hole. Mirrors test_softnet_20's
    # core allowed/blocked check.
    # ================================================================
    r = subprocess.run(
        [devm.path, "shell", "--", "curl", "-sf", "-o", "/dev/null",
         "-w", "%{http_code}", "--max-time", "15", "https://api.github.com/octocat"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=30,
    )
    assert r.returncode == 0 and r.stdout.strip() == b"200", (
        f"allow-listed host returned status {r.stdout!r} (stderr: {r.stderr.decode()}) "
        f"— ingress wiring must not have disturbed egress enforcement"
    )

    r = subprocess.run(
        [devm.path, "shell", "--", "curl", "-sf", "-o", "/dev/null",
         "--max-time", "15", "https://google.com"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=30,
    )
    assert r.returncode != 0, (
        "non-allow-listed host should have been blocked but curl "
        "returned 0 — ingress listeners opened an egress hole"
    )

    # ================================================================
    # Assertion 5: NO guest nftables — the old guest-side svc_ingress
    # ingress path (and its devm_* tables) is fully retired under
    # softnet; ingress flows entirely through softnet's own listeners.
    # ================================================================
    r = sandbox.exec("sudo", "nft", "list", "ruleset")
    combined = r.stdout + r.stderr
    assert "svc_ingress" not in combined, (
        f"guest nftables still has an svc_ingress chain (should be gone "
        f"under softnet):\n{combined}"
    )
    assert "devm_" not in combined, (
        f"guest nftables still has a devm_* table (should be gone under "
        f"softnet — softnet is the sole ingress/egress gate now):\n{combined}"
    )
