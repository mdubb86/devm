"""41: install/uninstall with Tart base image build + VM cold-start.

macOS + sudo gated. Slow: building the Tart base image takes 5-10 min.

What this pins:
  - `devm install` builds the devm-base Tart image and registers the launchd
    LaunchAgent (com.devm.service).
  - A cold-start via `devm shell -- true` proves the image build worked and
    that VM provisioning succeeds.
  - The VM is running after cold-start and exec-ready (tart_sandbox.state() ==
    "running", exec("true").exit_code == 0).
  - `devm teardown --yes` destroys the VM (state == "absent").
  - `devm uninstall --yes` removes the LaunchAgent plist and the runtime dir.

What it doesn't cover (covered by other tests):
  - DNS / CA trust / HTTPS proxy (test_39, test_40).
  - Stop-only (not destroy) paths (test_52).
  - Teardown in isolation (test_53).
"""
from __future__ import annotations

import os
import platform
import subprocess
import time

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


def _launchagent_plist_path() -> str:
    return os.path.expanduser(
        "~/Library/LaunchAgents/com.devm.service.plist"
    )


def _runtime_dir() -> str:
    return os.path.expanduser(
        "~/Library/Application Support/devm"
    )


@pytest.mark.slow
@pytest.mark.devm
@pytest.mark.timeout(900)  # image build can take up to 10 min; give headroom
def test_install_uninstall_with_tart(devm, workspace, sudo_capable):
    if platform.system() != "Darwin":
        pytest.skip("install/uninstall + Tart test runs on macOS only")

    # Pre-clean: ignore any prior state.
    subprocess.run([devm.path, "uninstall"], capture_output=True, timeout=30)

    # tart_sandbox fixture isn't used here because we drive install/uninstall
    # ourselves. We build a TartSandbox handle from the workspace's sandbox_name.
    sbx = TartSandbox(name=workspace.sandbox_name)

    try:
        # --- Step 1: install ---
        # Builds devm-base Tart image + registers LaunchAgent + starts service.
        # This is the long step: image build is 5-10 min on first run.
        r = subprocess.run(
            [devm.path, "install"],
            capture_output=True, timeout=780, check=False,
        )
        assert r.returncode == 0, (
            f"install failed:\nstdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )
        assert os.path.exists(_launchagent_plist_path()), (
            "LaunchAgent plist not created by install"
        )

        # --- Step 2: cold-start via `devm shell -- true` ---
        # This proves the image was built and provisioning works.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            capture_output=True, cwd=str(workspace.path), timeout=300, check=False,
        )
        assert r.returncode == 0, (
            f"cold-start shell failed:\nstdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )

        # --- Step 3: verify VM is running and exec-ready ---
        assert sbx.state() == "running", (
            f"VM should be running after cold-start; got {sbx.state()!r}"
        )
        result = sbx.exec("true")
        assert result.exit_code == 0, (
            f"exec true in VM failed: exit={result.exit_code}, "
            f"stderr={result.stderr!r}"
        )

        # --- Step 4: teardown ---
        devm.teardown(yes=True, timeout=60)

        # Allow a brief settle for Tart to remove the VM.
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if sbx.state() == "absent":
                break
            time.sleep(0.5)
        assert sbx.state() == "absent", (
            f"VM still present after teardown; state={sbx.state()!r}"
        )

    finally:
        # --- Step 5: uninstall ---
        # Best-effort: always run even if earlier steps failed.
        r = subprocess.run(
            [devm.path, "uninstall"],
            capture_output=True, timeout=30,
        )
        assert r.returncode == 0, (
            f"uninstall failed: {r.stderr.decode()!r}"
        )

        # LaunchAgent plist should be gone.
        # Allow brief settle for launchd to drain.
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            if not os.path.exists(_launchagent_plist_path()):
                break
            time.sleep(0.2)
        assert not os.path.exists(_launchagent_plist_path()), (
            "LaunchAgent plist still present after uninstall"
        )

        # Runtime dir should be gone (socket, CA, etc.).
        assert not os.path.exists(_runtime_dir()), (
            "devm runtime dir still present after uninstall"
        )
