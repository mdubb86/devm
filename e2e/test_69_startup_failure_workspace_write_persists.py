"""69: startup failure: file written to the workspace dir by a failing startup
service persists on the host.

Companion to test_67. Startup failures in the Tart era are systemd-
managed: a service with `restart: no` that exits non-zero is marked
failed by systemd, but the VM stays running (unlike install: failure,
which tears down the VM).

Because the VM stays alive, mutagen's sync session for the primary repo
stays alive too, so a write to $WORKSPACE (the guest-side primary repo
path) during the service's exec propagates to the Mac-side mirror at
`mirror_path(workspace.vm_name, workspace.bare_repo_label())`. This test
pins that property explicitly.

Setup:
  - install: pre-writes a helper script that writes to $WORKSPACE then
    exits 1. This avoids shell metacharacters in ExecStart=.
  - service "failsvc": exec's the helper script. restart: no (systemd
    won't retry).

After cold-start, verify the file is present on the HOST filesystem at
the primary repo's Mac-side mirror
(mirror_path(workspace.vm_name, workspace.bare_repo_label())/startup-wrote.txt).

Also verifies the host can read and remove the file (observes UID/mode
without hard-asserting the exact values, since the mirror's UID mapping
may vary by configuration).

Devm dependency: same property as test_67/test_68 for startup context.
"""
from __future__ import annotations

import os
import subprocess
import time

import pytest

from helpers.mutagen_e2e import mirror_path
from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_startup_failure_workspace_write_persists_on_host(workspace, devm, sandbox_name):
    # $WORKSPACE is the guest-side primary repo path (/home/devm/<label>),
    # mutagen-synced to the Mac-side mirror at
    # mirror_path(workspace.vm_name, workspace.bare_repo_label()) —
    # two distinct paths. The write, run inside the guest, targets
    # $WORKSPACE (guest-visible); the host-side read targets the Mac
    # mirror.
    marker_path = mirror_path(workspace.vm_name, workspace.bare_repo_label()) / "startup-wrote.txt"

    # Pre-write a helper script via install: to avoid shell metacharacters
    # in ExecStart= (exec: joins argv with spaces without quoting). The
    # script writes to $WORKSPACE (mutagen-synced to marker_path on the
    # Mac) and exits 1 so systemd marks failsvc as "failed".
    install_script = (
        "printf '#!/bin/sh\\necho STARTUP_FAILED > \"$WORKSPACE/startup-wrote.txt\"\\nexit 1\\n'"
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
        [devm.path, "start"],
        capture_output=True, cwd=str(workspace.path),
        timeout=300, check=False,
    )

    # VM should be running even though the service failed.
    current = sandbox.state()
    assert current == "running", (
        f"expected VM running despite startup failure; got {current!r}"
    )

    # Give systemd a moment to run (and fail) the service, then let
    # mutagen's live sync propagate the write to the Mac-side mirror.
    time.sleep(2)

    assert marker_path.exists(), (
        f"VM-side startup write not visible on host. "
        f"mutagen may not have synced the service's write to the Mac mirror."
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
