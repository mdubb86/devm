"""69: startup failure: file written to the workspace dir by a failing startup
service persists on the host.

Companion to test_67. Startup failures in the Tart era are systemd-
managed: a service with `restart: no` that exits non-zero is marked
failed by systemd, but the VM stays running (unlike install: failure,
which tears down the VM).

Because the VM stays alive, the virtio-fs workspace mount remains
active, so writes to the workspace directory during the service's exec
trivially persist on the host. This test pins that property explicitly.

Setup:
  - install: pre-writes a helper script that writes to the workspace dir
    then exits 1. This avoids shell metacharacters in ExecStart=.
  - service "failsvc": exec's the helper script. restart: no (systemd
    won't retry).

After cold-start, verify the file is present on the HOST filesystem
at workspace.path/startup-wrote.txt.

Also verifies the host can read and remove the file (observes UID/mode
without hard-asserting the exact values, since virtio-fs UID mapping
may vary by configuration).

Devm dependency: same property as test_67/test_68 for startup context.
"""
from __future__ import annotations

import os
import subprocess
import time

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
@pytest.mark.xfail(
    strict=False,
    reason=(
        "devm bug F: the workspace virtiofs share is passed to tart run via "
        "--dir but is never mounted inside the VM (no provisioner step and no "
        "injection script mounts it). The failing service's write to the "
        "workspace path goes to a non-existent path inside the VM; the file "
        "is never visible on the host. Remove xfail when bug F lands."
    ),
)
def test_startup_failure_workspace_write_persists_on_host(workspace, devm, sandbox_name):
    # The workspace is virtio-fs mounted in the VM at the same absolute path
    # as on the host. Use the concrete path so the service's exec doesn't
    # depend on $WORKSPACE_DIR being set in the systemd unit's environment.
    marker_path = workspace.path / "startup-wrote.txt"

    # Pre-write a helper script via install: to avoid shell metacharacters
    # in ExecStart= (exec: joins argv with spaces without quoting).
    # The script writes to the workspace path (accessible in VM via virtio-fs)
    # and exits 1 so systemd marks failsvc as "failed".
    install_script = (
        f"printf '#!/bin/sh\\necho STARTUP_FAILED > \"{marker_path}\"\\nexit 1\\n'"
        " > /tmp/run-failsvc.sh && chmod +x /tmp/run-failsvc.sh"
    )

    # Write config BEFORE cold-start so the provisioner deploys failsvc.
    workspace.write_devmyaml(
        install=[install_script],
        services={
            "failsvc": {
                "exec": ["/tmp/run-failsvc.sh"],
                "restart": "no",
            },
        },
    )

    sandbox = TartSandbox(name=sandbox_name)

    # Cold-start: provisioner runs install: (writes helper script), then
    # starts failsvc. failsvc exits 1 → systemd marks it "failed" →
    # provisioner returns immediately with error → devm shell exits non-zero.
    # The VM stays running (daemon doesn't stop it on provisioner failure).
    subprocess.run(
        [devm.path, "shell", "--", "true"],
        capture_output=True, cwd=str(workspace.path),
        timeout=300, check=False,
    )

    # VM should be running even though the service failed.
    assert sandbox.state() == "running", (
        f"expected VM running despite startup failure; got {sandbox.state()!r}"
    )

    # Give systemd a moment to run (and fail) the service.
    # The service exits immediately; the delay ensures the write is flushed
    # through virtio-fs to the host.
    time.sleep(2)

    assert marker_path.exists(), (
        f"VM-side startup write not visible on host. "
        f"The virtio-fs mount may not be flushing writes from the service."
    )

    content = marker_path.read_text()
    assert content.rstrip() == "STARTUP_FAILED", (
        f"host file content mismatch: got {content!r}"
    )

    # Document observed ownership.
    st = os.stat(marker_path)
    print(f"observed UID={st.st_uid} GID={st.st_gid} mode={oct(st.st_mode & 0o777)}")
    print(f"host process EUID={os.geteuid()}")

    # Host can remove without sudo.
    try:
        marker_path.unlink()
    except PermissionError as e:
        pytest.fail(
            f"host cannot remove startup-written file without sudo. "
            f"Observed UID={st.st_uid}, host EUID={os.geteuid()}. Error: {e}"
        )
    assert not marker_path.exists(), "unlink appeared to succeed but file still present"
