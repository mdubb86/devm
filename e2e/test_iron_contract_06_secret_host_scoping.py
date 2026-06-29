"""Pin: secret substitution is scoped to a secret's bound hosts.

With allowlist ["*"] (reach anything) but a secret bound only to one host,
the placeholder is swapped ONLY for requests to that host. A request to any
other host carries the literal placeholder — proving open reachability does
not widen secret scope.
"""
import http.client
import subprocess
import threading
from http.server import BaseHTTPRequestHandler, HTTPServer

import pytest

from helpers.iron_proxy import IronProxyConfig, free_ports, spawn


def _generate_ca(tmp_path):
    ca_cert = tmp_path / "ca.crt"
    ca_key = tmp_path / "ca.key"
    subprocess.run(
        ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
         "-keyout", str(ca_key), "-out", str(ca_cert), "-days", "1",
         "-subj", "/CN=test-ca",
         "-addext", "basicConstraints=critical,CA:TRUE",
         "-addext", "keyUsage=critical,keyCertSign,cRLSign,digitalSignature"],
        check=True, capture_output=True,
    )
    return ca_cert, ca_key


@pytest.mark.devm
def test_secret_scoped_to_bound_host(tmp_path):
    """Secret bound to api.allowed.test must NOT inject into a request to
    the local echo backend (127.0.0.1), even though allow_domains=["*"].

    The echo backend records what Authorization header it actually received.
    If scoping works, the header contains the literal placeholder, not the
    real value REALTOKEN.
    """
    received: dict[str, str | None] = {"authorization": None}

    class EchoHandler(BaseHTTPRequestHandler):
        def do_GET(self):
            received["authorization"] = self.headers.get("Authorization")
            self.send_response(200)
            self.end_headers()

        def log_message(self, *args, **kwargs):
            pass  # suppress access log noise

    backend_port = free_ports(1)[0]
    backend = HTTPServer(("127.0.0.1", backend_port), EchoHandler)
    backend_thread = threading.Thread(target=backend.serve_forever, daemon=True)
    backend_thread.start()

    try:
        ca_cert, ca_key = _generate_ca(tmp_path)
        http_port, https_port = free_ports(2)
        token = "__DEVM_SECRET_FOO__"
        cfg = IronProxyConfig(
            http_listen=f"127.0.0.1:{http_port}",
            https_listen=f"127.0.0.1:{https_port}",
            ca_cert_path=str(ca_cert),
            ca_key_path=str(ca_key),
            allow_domains=["*"],
            secret_tokens={token: "DEVM_SECRET_FOO"},
            # Secret is bound to api.allowed.test — a host we will NOT call.
            secret_hosts={token: ["api.allowed.test"]},
        )

        with spawn(cfg, env={"DEVM_SECRET_FOO": "REALTOKEN"}):
            # Send a request to 127.0.0.1 (our echo backend), NOT api.allowed.test.
            # The Host header is 127.0.0.1, which does not match api.allowed.test,
            # so iron-proxy must leave the placeholder un-swapped.
            conn = http.client.HTTPConnection("127.0.0.1", http_port, timeout=5)
            conn.request(
                "GET",
                f"http://127.0.0.1:{backend_port}/",
                headers={"Authorization": f"Bearer {token}"},
            )
            resp = conn.getresponse()
            resp.read()
            assert resp.status == 200, f"proxy returned {resp.status}"
    finally:
        backend.shutdown()

    # Decisive assertion: the echo backend received the LITERAL placeholder,
    # not the real value "REALTOKEN". This proves host scoping is enforced.
    assert received["authorization"] == f"Bearer {token}", (
        f"expected literal placeholder (no swap for unbound host), "
        f"got: {received['authorization']!r}"
    )
