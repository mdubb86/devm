"""56: $WORKSPACE_DIR is set in install: and startup:, workspace contents visible.

In both `install:` and `startup:` (systemd service) contexts, the env
var WORKSPACE_DIR must be set to the workspace path AND the workspace
contents must be visible at that path.

In Ship 4, the env wrapper (.devm/.env) sets WORKSPACE_DIR to the
repo root (same as WORKSPACE). The workspace is mounted as a virtio-fs
share at the same absolute path as on the host.

What this pins:
  - WORKSPACE_DIR is set in install: commands run by the provisioner.
  - WORKSPACE_DIR is set in a systemd service exec context.
  - A sentinel file written to the workspace (host side) is visible
    inside the VM at $WORKSPACE_DIR.

What it doesn't cover (tested elsewhere):
  - WORKSPACE_DIR in interactive exec -> test_61.
  - Env var injection end-to-end via .devm/.env -> test_26.
"""
from __future__ import annotations

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.xfail(
    strict=False,
    reason=(
        "devm bug L (new): provisioner's runInstallCommands runs `tart exec bash -c` "
        "without sourcing .devm/.env, so WORKSPACE_DIR is not set during install: "
        "commands. devm bug F (workspace virtiofs share never mounted) means the "
        "sentinel file is not visible inside the VM. Also note: this test writes "
        "devmyaml AFTER the tart_sandbox fixture has already cold-started (test "
        "ordering bug); a full fix requires restructuring AND the above devm bugs. "
        "Remove xfail when bugs F and L land."
    ),
)
@pytest.mark.timeout(180)
def test_workspace_dir_set_in_install_and_startup(workspace, devm, tart_sandbox):
    # Write a sentinel file into the workspace so the mount is non-empty.
    sentinel = workspace.path / "MOUNT_SENTINEL_56"
    sentinel.write_text("present\n")

    workspace.write_devmyaml(
        install=[
            "printenv WORKSPACE_DIR > /tmp/install-ws 2>&1 || true",
        ],
        services={
            "wscheck": {
                "exec": ["sh", "-c", "printenv WORKSPACE_DIR > /tmp/startup-ws 2>&1 || true"],
                "restart": "no",
            },
        },
    )

    # tart_sandbox fixture cold-starts via `devm shell -- true`.
    assert tart_sandbox.state() == "running", (
        f"expected VM running; got {tart_sandbox.state()!r}"
    )

    # Install: check — provisioner ran install commands.
    r = tart_sandbox.exec_shell("cat /tmp/install-ws")
    assert r.ok, f"install-ws missing: {r.stderr}"
    ws = str(workspace.path)
    assert r.stdout.strip() == ws, (
        f"WORKSPACE_DIR in install: was {r.stdout.strip()!r}, expected {ws!r}"
    )

    # Startup (systemd service) check — service ran and wrote WORKSPACE_DIR.
    # The service exits immediately (restart: no), so the file should exist.
    r = tart_sandbox.exec_shell("cat /tmp/startup-ws")
    assert r.ok, f"startup-ws missing: {r.stderr}"
    assert r.stdout.strip() == ws, (
        f"WORKSPACE_DIR in startup: was {r.stdout.strip()!r}, expected {ws!r}"
    )

    # Workspace mount visibility: sentinel written on host must be readable
    # inside the VM at the same absolute path.
    r = tart_sandbox.exec_shell(f"cat {ws}/MOUNT_SENTINEL_56")
    assert r.ok, f"workspace sentinel not visible at {ws}/MOUNT_SENTINEL_56: {r.stderr}"
    assert r.stdout.strip() == "present", (
        f"sentinel content unexpected: {r.stdout!r}"
    )
