"""Pin: empty allow-list (no allowlist transform) blocks all egress.

Iron-proxy v0.45.0 defaults to deny-all when no allowlist transform is
present. An HTTP request to an unknown host through the http_listen port
must result in a 4xx/5xx response — never a successful proxied response.

Implementation note: iron-proxy's MITM architecture means:
  - TLS to https_listen always succeeds (it mints a leaf cert regardless
    of the allowlist); the allowlist check fires at the HTTP request layer.
  - HTTP requests through http_listen return 502 Bad Gateway for denied hosts.
"""
import http.client
import subprocess

import pytest

from helpers.iron_proxy import IronProxyConfig, free_ports, spawn


def _generate_ca(tmp_path):
    """Generate a self-signed CA with extensions iron-proxy requires."""
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


@pytest.mark.contract
def test_default_deny_blocks_unknown(tmp_path):
    ca_cert, ca_key = _generate_ca(tmp_path)

    http_port, https_port = free_ports(2)
    cfg = IronProxyConfig(
        http_listen=f"127.0.0.1:{http_port}",
        https_listen=f"127.0.0.1:{https_port}",
        ca_cert_path=str(ca_cert),
        ca_key_path=str(ca_key),
        allow_domains=[],  # empty: no allowlist transform → deny all
    )

    with spawn(cfg):
        # HTTP request through http_listen: iron-proxy returns 502 for denied hosts.
        conn = http.client.HTTPConnection("127.0.0.1", http_port, timeout=5)
        try:
            conn.request(
                "GET",
                "http://evil.example.com/",
                headers={"Host": "evil.example.com"},
            )
            resp = conn.getresponse()
            assert resp.status in (400, 403, 502, 503), (
                f"expected blocked response, got {resp.status}"
            )
        except (ConnectionError, OSError, http.client.RemoteDisconnected):
            # iron-proxy may drop the connection entirely — also counts as blocking.
            pass
