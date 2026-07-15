"""73: /vm/status returns Running:true after daemon restart for a VM
that's still up in tart.

Pin: the daemon's supervisor key is (project_id, role) and the
adoption map is in-memory only; after a restart it has zero entries.
/vm/status with vm_name in the query must fall through to tart's list
and report the VM's actual state. Pinned by `664157c`.
"""
import http.client
import json
import socket
import subprocess
import time
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


def _unix_vm_status(name: str) -> dict:
    """Hit /vm/status on the daemon's Unix socket and return the parsed body."""
    sock_path = str(
        Path.home() / "Library" / "Application Support" / "devm" / "devm.sock"
    )

    class UnixHTTPConnection(http.client.HTTPConnection):
        def __init__(self, path: str):
            super().__init__("localhost")
            self._sock_path = path

        def connect(self) -> None:
            self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
            self.sock.connect(self._sock_path)

    conn = UnixHTTPConnection(sock_path)
    conn.request(
        "GET",
        f"/vm/status?name={name}",
    )
    resp = conn.getresponse()
    assert resp.status == 200, f"/vm/status returned HTTP {resp.status}"
    return json.loads(resp.read())


@pytest.mark.timeout(180)
def test_vm_status_discovers_from_tart_after_daemon_restart(
    workspace, devm, tart_sandbox, sandbox_name, devm_installed
):
    # tart_sandbox already cold-started the VM. Confirm it's running
    # via tart's view directly.
    assert tart_sandbox.state() == "running"

    # Restart the daemon. service restart shells out to sudo internally
    # for the launchctl kickstart; sudo_capable has already verified
    # /dev/tty is available for prompting.
    r = subprocess.run(
        [devm.path, "service", "restart"],
        capture_output=True, timeout=60,
    )
    assert r.returncode == 0, (
        f"service restart failed: {r.stderr.decode()!r}"
    )

    # Give the new daemon a moment to come back up (it waits for /health
    # internally, so by the time restart returns it's already serving).
    time.sleep(2)

    # Hit /vm/status via the daemon's Unix socket. The supervisor's
    # adoption map is empty post-restart, so the handler must fall
    # through to `tart list` to determine the VM's actual state.
    body = _unix_vm_status(sandbox_name)
    assert body["running"] is True, (
        f"vm_status reported not running after daemon restart; body={body}"
    )
    assert body["present"] is True, (
        f"vm_status reported not present after daemon restart; body={body}"
    )
