"""280: Reverse hairpin — mac_services and peer_services end-to-end.

Covers both features in one test since the two-project setup is the
expensive part. Project A declares:
  - mac_services: a real Python HTTPServer running on Mac loopback.
  - peer_services: project B.
Project B serves api.<slugB>.e2e.test from an in-guest service.

Assertions, all run from inside VM-A:
  1. curl http://<name>.host.test:<port>/ returns the Mac Python server's body.
     (Proves the softnet forwarder is armed for the declared Mac port.)
  2. curl https://api.<slugB>.e2e.test/ returns B's in-guest service body.
     (Proves A's SNI mux + splice reaches B's reverse-hairpin listener.)
  3. curl http://undeclared.host.test:9/ exits non-zero.
     (Proves an undeclared Mac port is not forwarded — fail-closed.)
  4. curl --max-time 3 https://unknown.other.e2e.test/ exits non-zero.
     (Proves an undeclared peer SNI is rejected — fail-closed.)
"""
from __future__ import annotations

import http.server
import socketserver
import subprocess
import threading

import pytest

from helpers.exec_retry import devm_exec_with_retry
from helpers.workspace import Workspace

pytestmark = pytest.mark.devm

MAC_BANNER = b"mac-service-ok\n"
PEER_BANNER = b"peer-service-ok\n"


class _MacHandler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/plain")
        self.send_header("Content-Length", str(len(MAC_BANNER)))
        self.end_headers()
        self.wfile.write(MAC_BANNER)

    def log_message(self, *_a):
        pass


@pytest.fixture
def mac_http_server():
    """Bind a Python HTTPServer on 127.0.0.1 with an OS-chosen port and
    serve a single banner. Yields the chosen port; shuts down cleanly.
    """
    srv = socketserver.TCPServer(("127.0.0.1", 0), _MacHandler)
    port = srv.server_address[1]
    t = threading.Thread(target=srv.serve_forever, daemon=True)
    t.start()
    try:
        yield port
    finally:
        srv.shutdown()
        srv.server_close()


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_reverse_hairpin_mac_and_peer_services(
    tmp_path, devm, sandbox_name, workspace, mac_http_server
):
    mac_port = mac_http_server
    slug_peer = f"{sandbox_name}-peer"
    peer_hostname = f"api.{slug_peer}.e2e.test"

    # Project A declares both features.
    workspace.write_devmyaml(
        no_repo=True,
        mac_services=[{"name": "studio", "port": mac_port}],
        peer_services=[{"project": slug_peer}],
    )

    # Project B: hand-roll a second workspace under tmp_path with its
    # own slug / vm_name. port_offset is bumped +1000 so the two
    # workspaces don't collide on host-port assignments.
    peer_dir = tmp_path / f"ws-{slug_peer}"
    peer_dir.mkdir()
    peer_ws = Workspace(
        peer_dir, slug=slug_peer, vm_name=slug_peer,
        port_offset=workspace.port_offset + 1000,
    )
    # `nc` isn't in the base image (see image/provision-base.sh's
    # install list); declare it via `packages:` (test_112's shape),
    # which needs the Debian mirrors allow-listed for the apt install
    # to reach them through iron-proxy. `-k` keeps nc listening across
    # connections, `-q 0` closes the socket at stdin EOF; the outer
    # while-loop restarts nc if `-lk` exits so repeat connections
    # (TLS handshake probe + request) all see the banner.
    peer_ws.write_devmyaml(
        no_repo=True,
        services={
            "api": {
                "port": 8080,
                "hostname": peer_hostname,
                "exec": [
                    "sh", "-c",
                    f"printf '%s' '{PEER_BANNER.decode()}' > /tmp/body && "
                    "while true; do nc -lk -p 8080 -q 0 < /tmp/body; done",
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

        # 1. mac_services: curl Mac Python server from inside VM-A.
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

        # 2. peer_services: curl peer's native hostname from inside VM-A.
        # devm installs the CA into the guest trust store, so no
        # --cacert is needed. Keep -fsS so curl's exit code reflects
        # HTTP-level failure too.
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

        # 3. Fail-closed: undeclared Mac port — forwarder isn't armed,
        # so the TCP connect gets RST'd. `--max-time 3` guards against
        # a mis-wired path turning this into a hang.
        r = devm_exec_with_retry(
            devm.path,
            ["curl", "--max-time", "3", "-sS",
             "http://undeclared.host.test:9/"],
            cwd=str(workspace.path),
        )
        assert r.returncode != 0, (
            f"undeclared port should fail, got rc={r.returncode} "
            f"stdout={r.stdout!r}"
        )

        # 4. Fail-closed: undeclared peer SNI — the SNI mux rejects
        # with `unrecognized_name` so curl surfaces a TLS error.
        r = devm_exec_with_retry(
            devm.path,
            ["curl", "--max-time", "3", "-sS",
             "https://unknown.other.e2e.test/"],
            cwd=str(workspace.path),
        )
        assert r.returncode != 0, (
            f"undeclared peer should fail, got rc={r.returncode} "
            f"stdout={r.stdout!r}"
        )
    finally:
        # Clean up both VMs. Must be `devm stop` — `tart stop` would
        # crash the guest and leave docker's --restart recovery broken.
        subprocess.run(
            [devm.path, "stop"], cwd=str(workspace.path), timeout=120,
        )
        subprocess.run(
            [devm.path, "stop"], cwd=str(peer_ws.path), timeout=120,
        )
