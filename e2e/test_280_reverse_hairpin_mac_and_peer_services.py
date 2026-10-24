"""280: Reverse hairpin — mac_services and peer_services end-to-end.

Covers both features in one test since the two-project setup is the
expensive part. Project A declares:
  - mac_services: a real Python HTTPServer running on Mac loopback.
  - peer_services: project B.
Project B serves api.<slugB>.e2e.test from an in-guest nc one-shot
responder that emits a complete HTTP/1.1 response.

Assertions, all run from inside VM-A:
  1. curl http://studio.host.test:<declared_port>/ returns the Mac
     Python server's body. (Softnet forwarder is armed for the
     declared Mac port.)
  2. curl https://api.<slugB>.e2e.test/ returns B's banner. (A's SNI
     mux + splice reaches B's reverse-hairpin listener; B's
     ReverseProxy forwards a valid HTTP response to the backend.)
  3. curl http://x.host.test:<undeclared_port>/ fails. The undeclared
     port is a REAL Mac listener (second fixture) — if softnet dropped
     its allowlist, this would succeed and the test would catch the
     regression. A local curl against 127.0.0.1 is used to prove the
     Mac listener is actually alive, so a "connection refused" rc
     cannot pass this check for the wrong reason.
  4. curl --max-time 3 https://stray.not-a-peer.e2e.test/ exits with
     curl rc 35 (TLS handshake error). First prove the hostname
     actually resolves (guest DNS routes *.e2e.test to softnet's
     InterceptedTestIP), so NXDOMAIN cannot masquerade as a mux
     reject.
"""
from __future__ import annotations

import http.server
import socketserver
import subprocess
import threading

import pytest

from helpers import registry
from helpers.exec_retry import devm_exec_with_retry
from helpers.workspace import Workspace

pytestmark = pytest.mark.devm

MAC_BANNER = b"mac-service-ok\n"
# PEER_BANNER has no trailing newline on purpose. The guest-side service
# embeds this byte-for-byte inside the systemd ExecStart= argument via a
# printf shell command; a literal LF in that argument would terminate the
# ExecStart directive and systemd would reject the unit as a bad setting.
# Keep this a single line of printable ASCII.
PEER_BANNER = b"peer-service-ok"


class _MacHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(MAC_BANNER)))
        self.end_headers()
        self.wfile.write(MAC_BANNER)

    def log_message(self, *_a):
        pass


def _spawn_mac_http_server():
    srv = socketserver.TCPServer(("127.0.0.1", 0), _MacHandler)
    port = srv.server_address[1]
    t = threading.Thread(target=srv.serve_forever, daemon=True)
    t.start()
    return srv, port


@pytest.fixture
def mac_http_server():
    """Mac loopback HTTPServer for the DECLARED mac_service.
    Serves the MAC_BANNER on every GET and shuts down cleanly.
    """
    srv, port = _spawn_mac_http_server()
    try:
        yield port
    finally:
        srv.shutdown()
        srv.server_close()


@pytest.fixture
def mac_undeclared_http_server():
    """Mac loopback HTTPServer on a DIFFERENT port, NOT in mac_services.

    Discriminator for assertion 3: if softnet's allowlist is dropped,
    curl from the guest will succeed against this port and the test
    will fail loud. A classic curl-against-port-9 check only proves
    nothing is listening — not that the allowlist is enforcing.
    """
    srv, port = _spawn_mac_http_server()
    try:
        yield port
    finally:
        srv.shutdown()
        srv.server_close()


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_reverse_hairpin_mac_and_peer_services(
    tmp_path, devm, sandbox_name, workspace,
    mac_http_server, mac_undeclared_http_server,
):
    mac_port = mac_http_server
    mac_undeclared_port = mac_undeclared_http_server
    slug_peer = f"{sandbox_name}-peer"
    peer_hostname = f"api.{slug_peer}.e2e.test"

    # Project A declares both features. Only `mac_port` is listed —
    # `mac_undeclared_port` is deliberately omitted so assertion 3
    # can prove softnet's allowlist actually blocks it.
    workspace.write_devmyaml(
        no_repo=True,
        mac_services=[{"name": "studio", "port": mac_port}],
        peer_services=[{"project": slug_peer}],
    )

    # Project B: hand-roll a second workspace under tmp_path with its
    # own slug / vm_name. port_offset is bumped +1000 so the two
    # workspaces don't collide on host-port assignments. Register the
    # peer sandbox with the crash-safe sweep BEFORE write/start so a
    # SIGKILL mid-test still gets its VM swept.
    peer_dir = tmp_path / f"ws-{slug_peer}"
    peer_dir.mkdir()
    peer_ws = Workspace(
        peer_dir, slug=slug_peer, vm_name=slug_peer,
        port_offset=workspace.port_offset + 1000,
    )
    registry.append("sandbox", slug_peer)

    # `nc` isn't in the base image (grep image/provision-base.sh's
    # install list — neither nc nor python3 ships). Declare via
    # `packages:` (test_112's shape), with the Debian mirrors on
    # network.allow so apt can reach them through iron-proxy.
    #
    # The exec serves a COMPLETE HTTP/1.1 response. B's hairpin
    # TLS-terminates at `api.<slug_peer>.e2e.test` and reverse-proxies
    # http://127.0.0.1:8080; Go's http.Transport requires a parseable
    # HTTP response, so raw bytes would 502. One-shot nc inside a
    # while-loop: each incoming connection fires a fresh nc with its
    # own pipe-stdin producing the response. `-q 1` keeps the socket
    # open 1s past stdin EOF so the response fully flushes before
    # close. No `-k` — the loop handles re-listening.
    peer_ws.write_devmyaml(
        no_repo=True,
        services={
            "api": {
                "port": 8080,
                "hostname": peer_hostname,
                "exec": [
                    "sh", "-c",
                    "while true; do "
                    "printf 'HTTP/1.1 200 OK\\r\\n"
                    f"Content-Length: {len(PEER_BANNER)}\\r\\n"
                    "Connection: close\\r\\n\\r\\n"
                    f"{PEER_BANNER.decode()}' | "
                    "nc -l -p 8080 -q 1; "
                    "done",
                ],
                "restart": "always",
            },
        },
        packages=["netcat-openbsd"],
        network={"allow": ["deb.debian.org", "security.debian.org"]},
    )

    def _start(ws):
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(ws.path),
            capture_output=True,
            timeout=300,
        )
        assert r.returncode == 0, (
            f"devm start {ws.slug} failed:\n"
            f"stdout={r.stdout.decode(errors='replace')!r}\n"
            f"stderr={r.stderr.decode(errors='replace')!r}"
        )

    try:
        # Peer first so its hairpin listener is up when A dials it.
        _start(peer_ws)
        _start(workspace)

        # 1. mac_services: curl the Mac Python server from inside VM-A.
        r = devm_exec_with_retry(
            devm.path,
            ["curl", "-fsS", f"http://studio.host.test:{mac_port}/"],
            cwd=str(workspace.path),
        )
        assert r.returncode == 0, (
            f"mac curl rc={r.returncode} stderr={r.stderr!r}"
        )
        assert r.stdout.strip() == MAC_BANNER.strip(), (
            f"mac body: {r.stdout!r}"
        )

        # 2. peer_services: curl peer's native hostname from inside
        # VM-A. devm installs the CA into the guest trust store, so no
        # --cacert needed. `-fsS` makes curl's exit code reflect HTTP
        # status too.
        r = devm_exec_with_retry(
            devm.path,
            ["curl", "-fsS", f"https://{peer_hostname}/"],
            cwd=str(workspace.path),
            timeout=60.0,
        )
        assert r.returncode == 0, (
            f"peer curl rc={r.returncode} stderr={r.stderr!r}"
        )
        assert r.stdout.strip() == PEER_BANNER.strip(), (
            f"peer body: {r.stdout!r}"
        )

        # 3. Fail-closed: undeclared Mac port. The target is a REAL
        # loopback listener on the Mac; softnet's allowlist should RST
        # the guest's TCP connect because the port is not in
        # mac_services. If the allowlist regressed, this curl would
        # succeed.
        r = devm_exec_with_retry(
            devm.path,
            ["curl", "--max-time", "3", "-sS",
             f"http://x.host.test:{mac_undeclared_port}/"],
            cwd=str(workspace.path),
        )
        assert r.returncode != 0, (
            f"undeclared port {mac_undeclared_port} should RST, "
            f"got rc={r.returncode} stdout={r.stdout!r}"
        )
        # Confirm the Mac listener is actually alive — if the local
        # curl can't reach it, the test above would have passed for the
        # wrong reason (dead backend, not enforced allowlist).
        local_check = subprocess.run(
            ["curl", "-fsS",
             f"http://127.0.0.1:{mac_undeclared_port}/"],
            capture_output=True, timeout=3,
        )
        assert local_check.returncode == 0, (
            "Mac listener on the undeclared port is dead — the "
            "fail-closed check above would have passed for the wrong "
            "reason."
        )

        # 4. Fail-closed: undeclared peer SNI. First prove DNS
        # resolves — softnet's in-guest DNS routes *.e2e.test to
        # InterceptedTestIP for every unknown project, so the guest
        # WILL open a TCP conn to the project IP and talk to A's own
        # HTTPS listener. The SNI mux then peeks the ClientHello, sees
        # `not-a-peer.e2e.test` is neither A's own zone nor a declared
        # peer, writes an unrecognized_name alert, and closes. curl
        # surfaces that as rc 35 (SSL connect error). Asserting rc 35
        # (vs just non-zero) excludes a DNS-level failure that would
        # mean the mux was never reached.
        stray = "stray.not-a-peer.e2e.test"
        dns_check = devm_exec_with_retry(
            devm.path,
            ["getent", "hosts", stray],
            cwd=str(workspace.path),
        )
        assert dns_check.returncode == 0, (
            f"DNS for undeclared peer {stray!r} must resolve before "
            f"the SNI reject assertion can be trusted; "
            f"got rc={dns_check.returncode} stderr={dns_check.stderr!r}"
        )
        r = devm_exec_with_retry(
            devm.path,
            ["curl", "--max-time", "3", "-sS",
             f"https://{stray}/"],
            cwd=str(workspace.path),
        )
        assert r.returncode == 35, (
            f"undeclared peer SNI should fail with curl rc 35 (TLS "
            f"handshake error from unrecognized_name alert); got "
            f"rc={r.returncode} stdout={r.stdout!r} stderr={r.stderr!r}"
        )
    finally:
        # Clean up both VMs. Each teardown gets its own try/except so a
        # TimeoutExpired on the first doesn't prevent the second from
        # running. Workspace fixture tears down A again via its own
        # finally (idempotent); the peer workspace needs this call
        # because no fixture owns it.
        for ws in (workspace, peer_ws):
            try:
                subprocess.run(
                    [devm.path, "teardown", "--yes"],
                    cwd=str(ws.path),
                    capture_output=True,
                    timeout=120,
                )
            except subprocess.TimeoutExpired:
                pass
        # Drop the peer sandbox registry entry; the sweep will
        # force-clean if teardown didn't finish removing the VM.
        registry.remove("sandbox", slug_peer)
