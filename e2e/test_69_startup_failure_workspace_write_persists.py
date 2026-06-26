"""69: startup failure: file written to $WORKSPACE_DIR by a failing startup
service persists on the host.

Companion to test_67. Startup failures in the Tart era are systemd-
managed: a service with `restart: no` that exits non-zero is marked
failed by systemd, but the VM stays running (unlike install: failure,
which tears down the VM).

Because the VM stays alive, the virtio-fs workspace mount remains
active, so writes to $WORKSPACE_DIR during the service's exec trivially
persist on the host. This test pins that property explicitly.

Setup:
  - service "failsvc": writes to $WORKSPACE_DIR/startup-wrote.txt, then
    exits 1. restart: no (so systemd doesn't retry).

After cold-start, verify the file is present on the HOST filesystem
at workspace.path/startup-wrote.txt.

Also verifies the host can read and remove the file (observes UID/mode
without hard-asserting the exact values, since virtio-fs UID mapping
may vary by configuration).

Devm dependency: same property as test_67/test_68 for startup context.
"""
from __future__ import annotations

import os
import time

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_startup_failure_workspace_write_persists_on_host(workspace, devm, tart_sandbox):
    workspace.write_devmyaml(
        services={
            "failsvc": {
                "exec": [
                    "sh", "-c",
                    'echo STARTUP_FAILED > "$WORKSPACE_DIR/startup-wrote.txt"; exit 1',
                ],
                "restart": "no",
            },
        },
    )

    # tart_sandbox fixture cold-starts. The VM should be running even
    # though the service failed (startup failure doesn't kill the VM).
    assert tart_sandbox.state() == "running", (
        f"expected VM running despite startup failure; got {tart_sandbox.state()!r}"
    )

    # Give systemd a moment to run (and fail) the service.
    # The service exits immediately; the delay ensures the write is flushed
    # through virtio-fs to the host.
    time.sleep(2)

    host_path = workspace.path / "startup-wrote.txt"
    assert host_path.exists(), (
        f"VM-side startup write to $WORKSPACE_DIR not visible on host. "
        f"The virtio-fs mount may not be flushing writes from the service."
    )

    content = host_path.read_text()
    assert content.rstrip() == "STARTUP_FAILED", (
        f"host file content mismatch: got {content!r}"
    )

    # Document observed ownership.
    st = os.stat(host_path)
    print(f"observed UID={st.st_uid} GID={st.st_gid} mode={oct(st.st_mode & 0o777)}")
    print(f"host process EUID={os.geteuid()}")

    # Host can remove without sudo.
    try:
        host_path.unlink()
    except PermissionError as e:
        pytest.fail(
            f"host cannot remove startup-written file without sudo. "
            f"Observed UID={st.st_uid}, host EUID={os.geteuid()}. Error: {e}"
        )
    assert not host_path.exists(), "unlink appeared to succeed but file still present"
