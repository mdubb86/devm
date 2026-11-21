"""Pin: iron-proxy reads config from a YAML file, binds the requested HTTP
port, and exits cleanly on SIGTERM.

iron-proxy v0.45.0 requires a file path via `-config path/to/file.yaml`;
stdin is NOT supported. The helper writes a temp YAML file and deletes it
after the context manager exits. See e2e/helpers/iron_proxy.py for details.

Foundation test — every other iron-proxy contract test + the daemon's
config builder depends on this shape working.

Config schema pinned here:
  dns.enabled:          false  (DNS listener disabled; avoids needing :53 / root)
  proxy.http_listen:    "127.0.0.1:<ephemeral>"
  proxy.https_listen:   "127.0.0.1:<ephemeral>"
  tls.ca_cert/ca_key:   paths to a test-generated self-signed CA
"""
import socket
import subprocess

import pytest

from helpers.iron_proxy import IronProxyConfig, free_ports, spawn


@pytest.mark.contract
def test_iron_proxy_starts_binds_and_exits_on_sigterm(tmp_path):
    # Generate a self-signed CA (iron-proxy requires one for TLS MITM mode).
    ca_cert = tmp_path / "ca.crt"
    ca_key = tmp_path / "ca.key"
    # iron-proxy validates that the CA has basicConstraints CA:TRUE and
    # KeyUsage keyCertSign. Generate with the required extensions.
    subprocess.run(
        [
            "openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes",
            "-keyout", str(ca_key), "-out", str(ca_cert),
            "-days", "1", "-subj", "/CN=test-ca",
            "-addext", "basicConstraints=critical,CA:TRUE",
            "-addext", "keyUsage=critical,keyCertSign",
        ],
        check=True,
        capture_output=True,
    )

    http_port, https_port = free_ports(2)
    cfg = IronProxyConfig(
        http_listen=f"127.0.0.1:{http_port}",
        https_listen=f"127.0.0.1:{https_port}",
        ca_cert_path=str(ca_cert),
        ca_key_path=str(ca_key),
        # DNS listener disabled: avoids needing privileged port 53.
        dns_enabled=False,
    )

    with spawn(cfg) as proc:
        # Pin: process is still alive after binding.
        assert proc.poll() is None, "iron-proxy exited before yield"

        # Pin: HTTP port is reachable.
        s = socket.create_connection(("127.0.0.1", http_port), timeout=5)
        s.close()

        # Pin: HTTPS port is reachable.
        s = socket.create_connection(("127.0.0.1", https_port), timeout=5)
        s.close()

    # Pin: process exited cleanly after SIGTERM.
    assert proc.poll() is not None, "iron-proxy didn't exit on SIGTERM"
