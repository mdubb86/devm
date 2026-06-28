"""Pin: glob *.npmjs.org in the allowlist permits registry.npmjs.org.

Iron-proxy v0.45.0 uses MITM architecture: for HTTPS traffic, the client
connects to https_listen with TLS (SNI = target hostname) and iron-proxy
presents a MITM leaf cert. The allowlist is enforced at the HTTP layer
after TLS.

To route HTTPS traffic through iron-proxy:
  1. Connect to https_listen (the MITM port) with TLS, SNI = target hostname.
  2. Send plain HTTP requests inside the TLS channel.

This test verifies that *.npmjs.org in the allowlist permits a GET to
registry.npmjs.org.

Skipped when registry.npmjs.org is not reachable (offline CI).
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
            "-days", "1", "-subj", "/CN=test-ca",
            "-addext", "basicConstraints=critical,CA:TRUE",
            "-addext", "keyUsage=critical,keyCertSign,cRLSign,digitalSignature",
        ],
        check=True,
        capture_output=True,
    )
    return ca_cert, ca_key


@pytest.mark.contract
@pytest.mark.skipif(
    subprocess.run(
        ["ping", "-c", "1", "-W", "2", "registry.npmjs.org"],
        capture_output=True,
    ).returncode != 0,
    reason="needs internet to registry.npmjs.org",
)
def test_glob_allow_list(tmp_path):
    ca_cert, ca_key = _generate_ca(tmp_path)

    http_port, https_port = free_ports(2)
    cfg = IronProxyConfig(
        http_listen=f"127.0.0.1:{http_port}",
        https_listen=f"127.0.0.1:{https_port}",
        ca_cert_path=str(ca_cert),
        ca_key_path=str(ca_key),
        allow_domains=["*.npmjs.org"],
    )

    with spawn(cfg):
        # Iron-proxy MITM: connect to https_listen with TLS (SNI = target),
        # then send plain HTTP inside the TLS channel. The allowlist fires
        # at the HTTP layer, so a 2xx/3xx means the host was allowed.
        raw = socket.create_connection(("127.0.0.1", https_port), timeout=10)
        ctx = ssl.create_default_context(cafile=str(ca_cert))
        ctx.check_hostname = False  # connecting to 127.0.0.1, not the target
        tls = ctx.wrap_socket(raw, server_hostname="registry.npmjs.org")
        tls.sendall(
            b"GET / HTTP/1.1\r\nHost: registry.npmjs.org\r\nConnection: close\r\n\r\n"
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

        status_line = response.split(b"\r\n")[0]
        status_code = int(status_line.split(b" ")[1])
        assert status_code in (200, 301, 302, 404), (
            f"upstream not reached: {status_code}"
        )
