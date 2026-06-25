"""38: devm install / start / status / curl /health / uninstall lifecycle.

Gated on macOS (LaunchAgent is Mac-only for Ship 1). Skipped elsewhere.

What this pins:
  - `devm install` registers a LaunchAgent and starts the service.
  - The service binds the Unix socket at the expected path with 0600 perms.
  - `curl --unix-socket … /health` returns 200.
  - `devm service-status` returns "running".
  - `devm uninstall` cleans up the LaunchAgent + socket file.

What it doesn't cover (later ships):
  - Service business logic (DNS, proxy, sandbox). Ship 1 service does nothing.
"""
from __future__ import annotations

import os
import platform
import socket
import stat
import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


def _socket_path() -> str:
    return os.path.expanduser(
        "~/Library/Application Support/devm/devm.sock"
    )


def _wait_socket(timeout: float = 10.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if os.path.exists(_socket_path()):
            return True
        time.sleep(0.2)
    return False


def _http_get_unix(path: str) -> tuple[int, str]:
    """GET path over the devm.sock Unix socket. Returns (status, body)."""
    sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
    sock.settimeout(2.0)
    sock.connect(_socket_path())
    sock.sendall(f"GET {path} HTTP/1.0\r\nHost: localhost\r\n\r\n".encode())
    chunks = []
    while True:
        b = sock.recv(4096)
        if not b:
            break
        chunks.append(b)
    sock.close()
    raw = b"".join(chunks).decode(errors="replace")
    head, _, body = raw.partition("\r\n\r\n")
    status_line = head.splitlines()[0]
    status = int(status_line.split()[1])
    return status, body


@pytest.mark.timeout(60)
def test_service_lifecycle(devm):
    if platform.system() != "Darwin":
        pytest.skip("devm service lifecycle test runs on macOS only")

    # Pre-condition: not installed. Best-effort cleanup of any leftover state.
    subprocess.run([devm.path, "uninstall"], capture_output=True, timeout=15)

    try:
        # Install.
        r = subprocess.run(
            [devm.path, "install"],
            capture_output=True, timeout=15, check=False,
        )
        assert r.returncode == 0, (
            f"install failed: stdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )

        # Wait for the socket to appear.
        assert _wait_socket(timeout=15), "socket never appeared"

        # Verify perms.
        mode = stat.S_IMODE(os.stat(_socket_path()).st_mode)
        assert mode == 0o600, f"socket perms are {oct(mode)}, expected 0o600"

        # service-status should say running.
        r = subprocess.run(
            [devm.path, "service-status"],
            capture_output=True, timeout=5,
        )
        assert r.returncode == 0
        assert "running" in r.stdout.decode()

        # curl /health → 200 ok.
        status, body = _http_get_unix("/health")
        assert status == 200, f"GET /health: {status}"
        assert "ok" in body

        # /version returns SOMETHING. Won't match the CLI's dev version
        # exactly because Version is "dev" in `go run`; just check the
        # field exists.
        status, body = _http_get_unix("/version")
        assert status == 200
        assert "version" in body

    finally:
        # Always uninstall to leave the system clean.
        r = subprocess.run(
            [devm.path, "uninstall"],
            capture_output=True, timeout=15,
        )
        assert r.returncode == 0, f"uninstall: {r.stderr.decode()!r}"
        # Socket should be gone after uninstall.
        # Allow brief settle for launchd to remove the plist + drain.
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            if not os.path.exists(_socket_path()):
                break
            time.sleep(0.2)
        assert not os.path.exists(_socket_path()), (
            "socket file still present after uninstall"
        )
