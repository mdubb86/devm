"""Pin: a bare "*" in allowlist domains matches every host — apex,
subdomain, arbitrary depth. Open mode relies on this.
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
        ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
         "-keyout", str(ca_key), "-out", str(ca_cert), "-days", "1",
         "-subj", "/CN=test-ca",
         "-addext", "basicConstraints=critical,CA:TRUE",
         "-addext", "keyUsage=critical,keyCertSign,cRLSign,digitalSignature"],
        check=True, capture_output=True,
    )
    return ca_cert, ca_key


@pytest.mark.devm
@pytest.mark.skipif(
    subprocess.run(["ping", "-c", "1", "-W", "2", "example.com"],
                   capture_output=True).returncode != 0,
    reason="needs internet to example.com",
)
def test_star_allows_apex_host(tmp_path):
    ca_cert, ca_key = _generate_ca(tmp_path)
    http_port, https_port = free_ports(2)
    cfg = IronProxyConfig(
        http_listen=f"127.0.0.1:{http_port}",
        https_listen=f"127.0.0.1:{https_port}",
        ca_cert_path=str(ca_cert),
        ca_key_path=str(ca_key),
        allow_domains=["*"],
    )
    with spawn(cfg):
        # apex host (no subdomain) through the MITM port
        raw = socket.create_connection(("127.0.0.1", https_port), timeout=10)
        ctx = ssl.create_default_context(cafile=str(ca_cert))
        ctx.check_hostname = False
        tls = ctx.wrap_socket(raw, server_hostname="example.com")
        tls.sendall(b"GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n")
        resp = b""
        while b"\r\n\r\n" not in resp:
            chunk = tls.recv(4096)
            if not chunk:
                break
            resp += chunk
        tls.close(); raw.close()
        status = int(resp.split(b"\r\n")[0].split(b" ")[1])
        assert status in (200, 301, 302, 404), f"apex host not reached under '*': {status}"
