"""39: install/uninstall with DNS — installs the .test resolver, resolves, uninstalls.

macOS-only. Requires sudo (the install/uninstall path writes/removes
/etc/resolver/test). Skips cleanly if sudo is unavailable or the test
runner isn't allowed to prompt for password.

What this pins:
  - `devm install` produces a working *.test resolver
  - A *.test name resolves to 127.0.0.1 via the system resolver
  - `dig @127.0.0.1 -p 51153` answers directly (cross-check the daemon)
  - `devm uninstall` removes /etc/resolver/test
"""
from __future__ import annotations

import os
import platform
import shutil
import socket
import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


def _resolver_file_path() -> str:
    return "/etc/resolver/test"


@pytest.mark.timeout(90)
def test_install_uninstall_with_dns(devm, sudo_capable):
    if platform.system() != "Darwin":
        pytest.skip("install/uninstall + DNS test runs on macOS only")
    if not shutil.which("dig"):
        pytest.skip("dig not installed; cannot cross-check the DNS responder")

    # Pre-clean: ignore any prior state.
    subprocess.run([devm.path, "uninstall"], capture_output=True, timeout=30)

    try:
        # Install — registers LaunchAgent + writes /etc/resolver/test.
        r = subprocess.run(
            [devm.path, "install"], capture_output=True, timeout=30, check=False,
        )
        assert r.returncode == 0, (
            f"install failed: stdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )
        assert os.path.exists(_resolver_file_path()), "resolver file not created"

        # Verify the file contents match what we expect.
        with open(_resolver_file_path()) as f:
            contents = f.read()
        assert contents == "nameserver 127.0.0.1\nport 51153\n", (
            f"unexpected resolver file contents: {contents!r}"
        )

        # Direct cross-check: dig the daemon's port.
        r = subprocess.run(
            ["dig", "@127.0.0.1", "-p", "51153", "anything.test", "+short"],
            capture_output=True, timeout=10,
        )
        assert "127.0.0.1" in r.stdout.decode(), (
            f"direct DNS query failed: {r.stdout.decode()!r}"
        )

        # System resolver path: resolve a *.test name via Python's
        # socket module. This is the path real apps will use.
        # Allow brief settle time for macOS resolver cache.
        time.sleep(1)
        ip = socket.gethostbyname("anything-system-probe.test")
        assert ip == "127.0.0.1", (
            f"system resolver returned {ip!r}, expected 127.0.0.1"
        )

    finally:
        # Uninstall — removes everything we installed.
        r = subprocess.run(
            [devm.path, "uninstall"], capture_output=True, timeout=30,
        )
        assert not os.path.exists(_resolver_file_path()), (
            "resolver file not removed by uninstall"
        )
