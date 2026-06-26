"""40: install/uninstall with HTTPS reverse proxy — installs, exercises the proxy, uninstalls.

devm install (with CA trust) → spin a local Go test server →
devm route local → curl http + https → assert 200 → kill backend →
assert 502 → devm uninstall.

macOS + sudo gated.
"""
from __future__ import annotations

import os
import platform
import shutil
import socket
import subprocess
import time
import urllib.request

import pytest

pytestmark = pytest.mark.devm


def _alloc_port() -> int:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def _spawn_backend(port: int, body: str) -> subprocess.Popen:
    """Tiny HTTP server returning `body` for every GET."""
    script = (
        "from http.server import HTTPServer, BaseHTTPRequestHandler\n"
        f"class H(BaseHTTPRequestHandler):\n"
        f"    def do_GET(self):\n"
        f"        self.send_response(200)\n"
        f"        self.send_header('Content-Type','text/plain')\n"
        f"        self.end_headers()\n"
        f"        self.wfile.write({body!r}.encode())\n"
        f"    def log_message(self,*a,**k): pass\n"
        f"HTTPServer(('127.0.0.1',{port}),H).serve_forever()\n"
    )
    return subprocess.Popen(["python3", "-c", script])


def _devm_yaml(workspace_path: str, project_id: str, hostname: str, port: int) -> None:
    path = os.path.join(workspace_path, "devm.yaml")
    with open(path, "w") as f:
        # port_offset: 0 — local mode routes to svc.Port directly,
        # not port + offset, so 0 is correct here. A nonzero offset
        # would just cause schema validation to fail when the
        # ephemeral backend port is high enough to overflow 65535.
        f.write(
            f"project:\n"
            f"  id: {project_id}\n"
            f"  sandbox_name: {project_id}-sbx\n"
            f"  port_offset: 0\n"
            f"services:\n"
            f"  api:\n"
            f"    hostname: {hostname}\n"
            f"    port: {port}\n"
        )


@pytest.mark.timeout(120)
def test_install_uninstall_with_proxy(devm, workspace, sudo_capable):
    if platform.system() != "Darwin":
        pytest.skip("install/uninstall + HTTPS proxy test runs on macOS only")
    if not shutil.which("python3"):
        pytest.skip("python3 not on PATH")
    if not shutil.which("curl"):
        pytest.skip("curl not on PATH")

    backend_port = _alloc_port()
    hostname = f"e2e-{backend_port}.test"

    # Pre-clean.
    subprocess.run([devm.path, "uninstall"], capture_output=True, timeout=30)

    backend = _spawn_backend(backend_port, "hello from backend")
    try:
        time.sleep(0.5)  # let backend bind

        # Install — writes resolver file + trusts CA.
        r = subprocess.run([devm.path, "install"], capture_output=True, timeout=30)
        assert r.returncode == 0, (
            f"install failed: stdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )

        # Allow daemon a moment to start and bind listeners.
        time.sleep(1.5)

        # Push the route in local mode (backend on Mac, not in VM).
        _devm_yaml(str(workspace.path), "e2e-proxy", hostname, backend_port)
        r = subprocess.run(
            [devm.path, "route", "local"],
            capture_output=True, cwd=str(workspace.path), timeout=10,
        )
        assert r.returncode == 0, (
            f"route local: {r.stdout.decode()!r} | {r.stderr.decode()!r}"
        )

        # macOS resolver cache settle.
        time.sleep(1)

        # HTTP — Python's urllib follows the system resolver (Ship 2)
        # and hits our proxy on :80, which forwards to the backend.
        try:
            resp = urllib.request.urlopen(f"http://{hostname}/", timeout=5)
            body = resp.read()
            assert resp.status == 200
            assert body == b"hello from backend", f"HTTP body: {body!r}"
        except Exception as e:
            pytest.fail(f"HTTP request failed: {e}")

        # HTTPS — curl uses macOS's System Keychain root cert bundle
        # by default, which now includes our devm Local CA (trusted by
        # `devm install`).
        r = subprocess.run(
            ["curl", "-sS", "--max-time", "5", f"https://{hostname}/"],
            capture_output=True,
        )
        assert r.returncode == 0, (
            f"curl https failed: code={r.returncode}, stderr={r.stderr.decode()!r}"
        )
        assert r.stdout == b"hello from backend", (
            f"HTTPS body: {r.stdout!r}"
        )

        # Kill backend; expect 502 with friendly message.
        backend.terminate()
        try:
            backend.wait(timeout=5)
        except subprocess.TimeoutExpired:
            backend.kill()
        backend = None

        time.sleep(0.5)

        r = subprocess.run(
            ["curl", "-sS", "--max-time", "5", "-o", "-", "-w", "%{http_code}",
             f"http://{hostname}/"],
            capture_output=True,
        )
        out = r.stdout.decode()
        # Last 3 chars are the HTTP status code from curl's -w.
        assert out.endswith("502"), f"unexpected status: {out}"
        assert "no service listening" in out, f"no diagnostic body: {out}"

    finally:
        if backend is not None:
            backend.terminate()
            try:
                backend.wait(timeout=5)
            except subprocess.TimeoutExpired:
                backend.kill()

        # Uninstall — removes resolver file, removes CA from keychain.
        subprocess.run([devm.path, "uninstall"], capture_output=True, timeout=30)
        # Best-effort cleanup; primary assertion is no leftover state.
        assert not os.path.exists("/etc/resolver/test"), \
            "resolver file lingered after uninstall"
