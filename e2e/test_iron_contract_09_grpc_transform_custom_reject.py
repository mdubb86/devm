"""Pin: iron-proxy's grpc transform delegates allow/deny to an external
TransformService and delivers its custom reject response to clients
verbatim.

This is the upstream contract the grpc-policy-authority design stands on
(docs/superpowers/specs/2026-08-29-grpc-policy-authority-design.md): the
devm daemon replaces the built-in allowlist transform with a `grpc`
transform pointed at a per-project unix socket, becoming the realtime
egress policy authority with full control of what a blocked client sees.

Pinned here, against the real embedded iron-proxy binary and a stub
TransformService (e2e/contract/ironproxy/policyd):

1. REJECT's custom response — status, headers (X-Devm-Blocked), JSON
   body — reaches the client verbatim on the plain-HTTP listener.
2. Same on the HTTPS MITM listener, and iron-proxy still mints a
   CA-signed leaf cert for the DENIED host (so a CA-trusting guest
   reads the reject as clean HTTP, not a TLS failure).
3. CONTINUE lets the request through, and a downstream `secrets`
   transform still substitutes tokens — policy delegation does not
   disturb secret injection.
4. TransformService unreachable → fail-closed 502 (request never
   forwarded); once the service (re)appears on the socket, iron-proxy's
   lazy redial recovers without a proxy restart.

The stub speaks unix-socket gRPC ("unix:///path"); macOS caps
sun_path at 104 bytes, so sockets live in a short mkdtemp dir, never
under pytest's deep tmp_path.
"""
from __future__ import annotations

import contextlib
import http.client
import json
import shutil
import socket
import ssl
import subprocess
import tempfile
import threading
import time
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path

import pytest

from helpers.iron_proxy import IronProxyConfig, free_ports, spawn

_POLICYD_MODULE = Path(__file__).resolve().parent / "contract" / "ironproxy"


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


@pytest.fixture(scope="module")
def policyd_bin(tmp_path_factory) -> str:
    """Build the TransformService stub once per module."""
    out = tmp_path_factory.mktemp("policyd") / "policyd"
    subprocess.run(
        ["go", "build", "-o", str(out), "./policyd"],
        cwd=_POLICYD_MODULE,
        check=True,
        capture_output=True,
    )
    return str(out)


@contextlib.contextmanager
def _short_socket_dir():
    """A mkdtemp dir short enough for macOS's 104-byte sun_path cap."""
    d = tempfile.mkdtemp(prefix="ironpol-")
    try:
        yield Path(d)
    finally:
        shutil.rmtree(d, ignore_errors=True)


@contextlib.contextmanager
def _policyd(policyd_bin: str, sock: Path, allow: str, timeout: float = 10.0):
    """Run the stub TransformService on a unix socket; wait until it accepts."""
    proc = subprocess.Popen(
        [policyd_bin, "-sock", str(sock), "-allow", allow],
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    try:
        deadline = time.monotonic() + timeout
        while time.monotonic() < deadline:
            if proc.poll() is not None:
                out, err = proc.communicate()
                raise RuntimeError(
                    f"policyd exited early (rc={proc.returncode})\n"
                    f"stderr: {err.decode(errors='replace')}"
                )
            try:
                with socket.socket(socket.AF_UNIX, socket.SOCK_STREAM) as s:
                    s.settimeout(0.5)
                    s.connect(str(sock))
                    break
            except OSError:
                time.sleep(0.05)
        else:
            raise RuntimeError(f"policyd never bound {sock}")
        yield proc
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except subprocess.TimeoutExpired:
            proc.kill()


def _echo_backend():
    """Loopback HTTP server recording the last Authorization header."""
    received: dict[str, str | None] = {"authorization": None}

    class EchoHandler(BaseHTTPRequestHandler):
        def do_GET(self):
            received["authorization"] = self.headers.get("Authorization")
            self.send_response(200)
            self.end_headers()

        def log_message(self, *args, **kwargs):
            pass

    port = free_ports(1)[0]
    server = HTTPServer(("127.0.0.1", port), EchoHandler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
    return server, port, received


def _proxy_cfg(tmp_path, sock: Path) -> IronProxyConfig:
    ca_cert, ca_key = _generate_ca(tmp_path)
    http_port, https_port = free_ports(2)
    return IronProxyConfig(
        http_listen=f"127.0.0.1:{http_port}",
        https_listen=f"127.0.0.1:{https_port}",
        ca_cert_path=str(ca_cert),
        ca_key_path=str(ca_key),
        grpc_target=f"unix://{sock}",
    )


def _get_via_http_listener(port: int, url: str) -> http.client.HTTPResponse:
    conn = http.client.HTTPConnection("127.0.0.1", port, timeout=10)
    conn.request("GET", url)
    return conn.getresponse()


@pytest.mark.contract
def test_custom_reject_reaches_client_http(tmp_path, policyd_bin):
    """REJECT's status/header/body arrive verbatim; CONTINUE proxies."""
    backend, backend_port, _ = _echo_backend()
    try:
        with _short_socket_dir() as sockdir:
            sock = sockdir / "p.sock"
            cfg = _proxy_cfg(tmp_path, sock)
            http_port = int(cfg.http_listen.rsplit(":", 1)[1])
            with _policyd(policyd_bin, sock, allow="127.0.0.1"), spawn(cfg):
                # Denied host: the stub's custom response, verbatim.
                resp = _get_via_http_listener(
                    http_port, "http://blocked.example/some/path"
                )
                body = resp.read()
                assert resp.status == 451, f"expected custom 451, got {resp.status}"
                assert resp.getheader("X-Devm-Blocked") == "egress-policy"
                payload = json.loads(body)
                assert payload["blocked_by"] == "devm-egress-policy"
                assert payload["host"] == "blocked.example"

                # Allowed host: CONTINUE → proxied to the loopback backend.
                resp = _get_via_http_listener(
                    http_port, f"http://127.0.0.1:{backend_port}/"
                )
                assert resp.status == 200, (
                    f"allowed host must proxy through, got {resp.status}"
                )
    finally:
        backend.shutdown()


@pytest.mark.contract
def test_custom_reject_reaches_client_https_mitm(tmp_path, policyd_bin):
    """The MITM listener mints a leaf cert even for a DENIED host, then
    serves the custom reject inside the tunnel — a CA-trusting client
    sees clean HTTP, never a TLS failure."""
    with _short_socket_dir() as sockdir:
        sock = sockdir / "p.sock"
        cfg = _proxy_cfg(tmp_path, sock)
        https_port = int(cfg.https_listen.rsplit(":", 1)[1])
        with _policyd(policyd_bin, sock, allow="127.0.0.1"), spawn(cfg):
            raw = socket.create_connection(("127.0.0.1", https_port), timeout=10)
            ctx = ssl.create_default_context(cafile=cfg.ca_cert_path)
            ctx.check_hostname = False  # connecting to 127.0.0.1, not the target
            # Raises SSLCertVerificationError if no CA-signed leaf is minted
            # for the denied name — that's half the pin.
            tls = ctx.wrap_socket(raw, server_hostname="blocked.example")
            tls.sendall(
                b"GET /x HTTP/1.1\r\nHost: blocked.example\r\n"
                b"Connection: close\r\n\r\n"
            )
            response = b""
            while True:
                chunk = tls.recv(4096)
                if not chunk:
                    break
                response += chunk
            tls.close()
            raw.close()

            head, _, body = response.partition(b"\r\n\r\n")
            status_code = int(head.split(b"\r\n")[0].split(b" ")[1])
            assert status_code == 451, f"expected custom 451, got:\n{response!r}"
            assert b"x-devm-blocked: egress-policy" in head.lower()
            assert b'"blocked_by":"devm-egress-policy"' in body


@pytest.mark.contract
def test_continue_composes_with_secrets_transform(tmp_path, policyd_bin):
    """A downstream secrets transform still substitutes after the grpc
    transform CONTINUEs — policy delegation leaves injection intact."""
    backend, backend_port, received = _echo_backend()
    try:
        with _short_socket_dir() as sockdir:
            sock = sockdir / "p.sock"
            cfg = _proxy_cfg(tmp_path, sock)
            cfg.secret_tokens = {"__DEVM_SECRET_FOO__": "DEVM_SECRET_FOO"}
            http_port = int(cfg.http_listen.rsplit(":", 1)[1])
            with _policyd(policyd_bin, sock, allow="127.0.0.1"), spawn(
                cfg, env={"DEVM_SECRET_FOO": "real-secret-value"}
            ):
                conn = http.client.HTTPConnection("127.0.0.1", http_port, timeout=10)
                conn.request(
                    "GET",
                    f"http://127.0.0.1:{backend_port}/",
                    headers={"Authorization": "Bearer __DEVM_SECRET_FOO__"},
                )
                resp = conn.getresponse()
                assert resp.status == 200, f"proxy returned {resp.status}"
        assert received["authorization"] == "Bearer real-secret-value", (
            f"secrets transform must still substitute after grpc CONTINUE; "
            f"backend saw: {received['authorization']!r}"
        )
    finally:
        backend.shutdown()


@pytest.mark.contract
def test_fail_closed_then_recovers(tmp_path, policyd_bin):
    """TransformService down → 502, never forwarded; service appearing on
    the socket → lazy redial recovers without restarting iron-proxy."""
    with _short_socket_dir() as sockdir:
        sock = sockdir / "p.sock"
        cfg = _proxy_cfg(tmp_path, sock)
        http_port = int(cfg.http_listen.rsplit(":", 1)[1])
        with spawn(cfg):
            # No policyd yet: fail-closed for any host.
            resp = _get_via_http_listener(http_port, "http://blocked.example/")
            resp.read()
            assert resp.status == 502, (
                f"expected fail-closed 502 with service down, got {resp.status}"
            )

            # Service appears; iron-proxy must recover via lazy redial.
            with _policyd(policyd_bin, sock, allow="127.0.0.1"):
                deadline = time.monotonic() + 15
                last = None
                while time.monotonic() < deadline:
                    resp = _get_via_http_listener(
                        http_port, "http://blocked.example/"
                    )
                    resp.read()
                    last = resp.status
                    if last == 451:
                        break
                    time.sleep(0.5)
                assert last == 451, (
                    f"iron-proxy must redial the policy socket and serve the "
                    f"custom reject again; last status: {last}"
                )
