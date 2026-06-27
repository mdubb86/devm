"""Pin: iron-proxy mints leaf certs signed by the configured CA.

Iron-proxy v0.45.0 uses MITM architecture: when a client connects to
https_listen with TLS (SNI = target hostname), iron-proxy generates a
leaf certificate for that hostname signed by the configured CA.

This test verifies that:
1. A client that trusts ONLY the configured CA can verify the leaf cert.
2. The TLS handshake succeeds (proving our CA signed the leaf).

CA generation requires basicConstraints=CA:TRUE and keyCertSign in keyUsage
or iron-proxy will refuse to load the CA.

How the MITM works:
  - Connect to https_listen (the MITM port) with TLS, SNI = target hostname.
  - Iron-proxy presents a fresh leaf cert for that hostname, signed by
    the configured CA.
  - Send plain HTTP requests inside the TLS channel.
  - The TLS handshake validates against our CA (not the real upstream's CA).

Skipped when httpbin.org is not reachable (offline CI).
"""
import socket
import ssl
import subprocess

import pytest

from helpers.iron_proxy import IronProxyConfig, free_ports, spawn


def _generate_ca(tmp_path):
    ca_cert = tmp_path / "ca.crt"
    ca_key = tmp_path / "ca.key"
    subprocess.run(
        [
            "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
            "-keyout", str(ca_key), "-out", str(ca_cert),
            "-days", "1", "-subj", "/CN=devm-test-ca",
            "-addext", "basicConstraints=critical,CA:TRUE",
            "-addext", "keyUsage=critical,keyCertSign,cRLSign,digitalSignature",
        ],
        check=True,
        capture_output=True,
    )
    return ca_cert, ca_key


@pytest.mark.devm
@pytest.mark.skipif(
    subprocess.run(
        ["ping", "-c", "1", "-W", "2", "httpbin.org"],
        capture_output=True,
    ).returncode != 0,
    reason="needs internet to httpbin.org",
)
def test_leaf_cert_signed_by_ca(tmp_path):
    ca_cert, ca_key = _generate_ca(tmp_path)

    http_port, https_port = free_ports(2)
    cfg = IronProxyConfig(
        http_listen=f"127.0.0.1:{http_port}",
        https_listen=f"127.0.0.1:{https_port}",
        ca_cert_path=str(ca_cert),
        ca_key_path=str(ca_key),
        allow_domains=["httpbin.org"],
    )

    with spawn(cfg):
        # Use a context that trusts ONLY our test CA. If iron-proxy minted
        # the leaf cert with a different CA the TLS handshake will fail.
        # Iron-proxy MITM: connect to https_listen with SNI = target hostname.
        raw = socket.create_connection(("127.0.0.1", https_port), timeout=10)
        ctx = ssl.create_default_context(cafile=str(ca_cert))
        ctx.check_hostname = False  # connecting to 127.0.0.1, not the target
        # This will raise ssl.SSLCertVerificationError if the leaf cert is not
        # signed by our CA — that's the pin we're asserting.
        tls = ctx.wrap_socket(raw, server_hostname="httpbin.org")

        # Send an HTTP request through the MITM channel to confirm a live
        # round-trip, not just a TLS handshake.
        tls.sendall(
            b"GET /get HTTP/1.1\r\nHost: httpbin.org\r\nConnection: close\r\n\r\n"
        )
        response = b""
        while True:
            chunk = tls.recv(4096)
            if not chunk:
                break
            response += chunk
            if b"\r\n\r\n" in response:
                break
        tls.close()
        raw.close()

        # TLS handshake success + HTTP response proves:
        # (a) leaf cert is signed by our CA, and
        # (b) iron-proxy proxied the request to the real upstream.
        status_line = response.split(b"\r\n")[0]
        status_code = int(status_line.split(b" ")[1])
        assert status_code == 200, f"upstream not reached: {status_code}"
