"""Pin: iron-proxy substitutes opaque tokens with real secret values.

The `secrets` transform in iron-proxy v0.45.0 intercepts outbound
requests, finds the opaque proxy_value token in configured headers, and
replaces it with the real secret sourced from an env var. The real value
NEVER appears in the YAML config on disk.

Shape pinned:
  transforms:
    - name: secrets
      config:
        secrets:
          - source:
              type: env
              var: DEVM_SECRET_FOO
            proxy_value: __DEVM_SECRET_FOO__
            match_headers: [Authorization]
            rules:
              - host: "*"

The test:
1. Starts a loopback HTTP echo server that records the Authorization header.
2. Spawns iron-proxy configured with the secrets transform and the real
   value injected via env (DEVM_SECRET_FOO=real-secret-value).
3. Sends a request through the proxy's HTTP port with
   Authorization: Bearer __DEVM_SECRET_FOO__
4. Asserts the echo server received Authorization: Bearer real-secret-value
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
        [
            "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
            "-keyout", str(ca_key), "-out", str(ca_cert),
            "-days", "1", "-subj", "/CN=test-ca",
            "-addext", "basicConstraints=critical,CA:TRUE",
            "-addext", "keyUsage=critical,keyCertSign,cRLSign,digitalSignature",
        ],
        check=True,
        capture_output=True,
    )
    return ca_cert, ca_key


@pytest.mark.devm
def test_token_substitution(tmp_path):
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

        # Token "__DEVM_SECRET_FOO__" in the request → env var DEVM_SECRET_FOO
        # → "real-secret-value" at the upstream.
        cfg = IronProxyConfig(
            http_listen=f"127.0.0.1:{http_port}",
            https_listen=f"127.0.0.1:{https_port}",
            ca_cert_path=str(ca_cert),
            ca_key_path=str(ca_key),
            allow_domains=["127.0.0.1"],
            secret_tokens={"__DEVM_SECRET_FOO__": "DEVM_SECRET_FOO"},
        )

        # Real secret value goes into the iron-proxy process's env only —
        # it never appears in the YAML config on disk.
        with spawn(cfg, env={"DEVM_SECRET_FOO": "real-secret-value"}):
            conn = http.client.HTTPConnection(
                "127.0.0.1", http_port, timeout=5
            )
            conn.request(
                "GET",
                f"http://127.0.0.1:{backend_port}/",
                headers={"Authorization": "Bearer __DEVM_SECRET_FOO__"},
            )
            resp = conn.getresponse()
            assert resp.status == 200, f"proxy returned {resp.status}"

        assert received["authorization"] == "Bearer real-secret-value", (
            f"expected substituted secret, got: {received['authorization']!r}"
        )
    finally:
        backend.shutdown()
