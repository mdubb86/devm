"""56: $WORKSPACE is set in install: and startup:, workspace contents visible.

In both `install:` and `startup:` (systemd service) contexts, the env
var WORKSPACE must be set to the workspace path AND the workspace
contents must be visible at that path.

devm's schema (internal/schema/env.go) reserves WORKSPACE as the single
canonical name for the workspace path (also used for $WORKSPACE
substitution in devm.yaml). It's rendered into .devm/.env by the render
package; the with-devm-env wrapper sources it. Install: commands and
systemd services both go through the wrapper (Bug L + provisioner).

What this pins:
  - WORKSPACE is set in install: commands run by the provisioner.
  - WORKSPACE is set in a systemd service exec context.
  - A sentinel file written to the workspace (host side) is visible
    inside the VM at $WORKSPACE.

What it doesn't cover (tested elsewhere):
  - WORKSPACE in interactive exec -> test_61.
  - Env var injection end-to-end via .devm/.env -> test_26.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_workspace_set_in_install_and_startup(workspace, devm, sandbox_name):
    # Write a sentinel file into the workspace so the mount is non-empty.
    sentinel = workspace.path / "MOUNT_SENTINEL_56"
    sentinel.write_text("present\n")

    workspace.write_devmyaml(
        install=[
            "printenv WORKSPACE > /tmp/install-ws 2>&1 || true",
        ],
        services={
            "wscheck": {
                "exec": ["sh", "-c", "printenv WORKSPACE > /tmp/startup-ws 2>&1 || true"],
                "restart": "no",
            },
        },
    )

    # Owns cold-start: install: commands only run at first `devm shell`, so
    # the test itself triggers cold-start after devm.yaml is in place.
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    tart_sandbox = TartSandbox(name=sandbox_name)
    current = tart_sandbox.state()
    assert current == "running", f"expected VM running; got {current!r}"

    ws = str(workspace.path)

    # Install: check — provisioner ran install commands with .devm/.env sourced.
    r = tart_sandbox.exec_shell("cat /tmp/install-ws")
    assert r.ok, f"install-ws missing: {r.stderr}"
    assert r.stdout.strip() == ws, (
        f"WORKSPACE in install: was {r.stdout.strip()!r}, expected {ws!r}"
    )

    # Startup (systemd service) check — service ran and wrote WORKSPACE.
    # The service exits immediately (restart: no).
    r = tart_sandbox.exec_shell("cat /tmp/startup-ws")
    assert r.ok, f"startup-ws missing: {r.stderr}"
    assert r.stdout.strip() == ws, (
        f"WORKSPACE in startup: was {r.stdout.strip()!r}, expected {ws!r}"
    )

    # Workspace mount visibility: sentinel written on host must be readable
    # inside the VM at the same absolute path.
    r = tart_sandbox.exec_shell(f"cat {ws}/MOUNT_SENTINEL_56")
    assert r.ok, f"workspace sentinel not visible at {ws}/MOUNT_SENTINEL_56: {r.stderr}"
    assert r.stdout.strip() == "present", (
        f"sentinel content unexpected: {r.stdout!r}"
    )
