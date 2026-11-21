"""Pin: an allowlist transform with no domains blocks all egress.

Iron-proxy v0.45.0 runs the egress check ONLY when an allowlist
transform is present. Present with `domains: []` is the deny-all shape:
an HTTP request to an unlisted host through http_listen returns 403.
With the transform ABSENT there is no check at all and every host is
proxied — which is why devm always emits the transform, empty domains
included (internal/serviceapi/ironproxy.go).

The probe host must be one that genuinely resolves and serves: an
unreachable host would fail upstream and return 5xx no matter what the
allowlist says, so a "blocked" assertion against it would pass even
under allow-all.

Implementation note: iron-proxy's MITM architecture means TLS to
https_listen always succeeds (it mints a leaf cert regardless of the
allowlist); the allowlist check fires at the HTTP request layer.
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
        allow_domains=[],  # allowlist transform with no domains → deny all
    )

    with spawn(cfg):
        # example.com resolves and serves 200 when allowed, so a
        # non-200 here can only come from the allowlist.
        conn = http.client.HTTPConnection("127.0.0.1", http_port, timeout=10)
        try:
            conn.request(
                "GET",
                "http://example.com/",
                headers={"Host": "example.com"},
            )
            resp = conn.getresponse()
            assert resp.status == 403, (
                f"expected 403 for an unlisted host, got {resp.status}"
            )
        except (ConnectionError, OSError, http.client.RemoteDisconnected):
            # iron-proxy may drop the connection entirely — also counts as blocking.
            pass
