"""41: install/uninstall with Tart base image build + VM cold-start.

macOS + sudo gated. Slow: building the Tart base image takes 5-10 min.

What this pins:
  - `devm install` builds the devm-base Tart image and registers the launchd
    LaunchDaemon (com.devm.service) at /Library/LaunchDaemons/.
  - A cold-start via `devm shell -- true` proves the image build worked and
    that VM provisioning succeeds.
  - The VM is running after cold-start and exec-ready (tart_sandbox.state() ==
    "running", exec("true").exit_code == 0).
  - `devm teardown --yes` destroys the VM (state == "absent").
  - `devm uninstall --yes` removes the LaunchDaemon plist and the runtime dir.
  - Ship 4.2: plist lives at /Library/LaunchDaemons/ (system scope), the old
    ~/Library/LaunchAgents/ location is absent, launchctl shows the service
    running in system scope, and the daemon process owner is the invoking user.

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
from pathlib import Path

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


_LAUNCH_DAEMON_PLIST = Path("/Library/LaunchDaemons/com.devm.service.plist")
_LAUNCH_AGENT_PLIST = Path("~/Library/LaunchAgents/com.devm.service.plist").expanduser()


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
    # ourselves. We build a TartSandbox handle from the workspace's vm_name.
    vm = TartSandbox(name=workspace.vm_name)

    try:
        # --- Step 1: install ---
        # Builds devm-base Tart image + registers LaunchDaemon + starts service.
        # This is the long step: image build is 5-10 min on first run.
        r = subprocess.run(
            [devm.path, "install"],
            capture_output=True, timeout=780, check=False,
        )
        assert r.returncode == 0, (
            f"install failed:\nstdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )

        # Ship 4.2: plist is now a system-level LaunchDaemon, not a user LaunchAgent.
        assert _LAUNCH_DAEMON_PLIST.exists(), \
            "LaunchDaemon plist not installed at /Library/LaunchDaemons/"
        assert not _LAUNCH_AGENT_PLIST.exists(), \
            "old LaunchAgent plist should not be present after Ship 4.2 install"

        # launchctl shows the service in the system scope. Poll — install
        # returns as soon as launchd accepts the load, but the process
        # transitions through `state = xpcproxy` before reaching `running`.
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            r = subprocess.run(["launchctl", "print", "system/com.devm.service"],
                               capture_output=True, text=True, timeout=10)
            if r.returncode == 0 and "state = running" in r.stdout:
                break
            time.sleep(0.25)
        assert r.returncode == 0, f"launchctl print failed: {r.stderr}"
        assert "state = running" in r.stdout, (
            f"daemon didn't reach `state = running` within 10s of install:\n{r.stdout}"
        )

        # Daemon runs as the user (UserName key in the plist), not root.
        # Parse `launchctl print` for the pid, then check owner.
        pid_line = [l for l in r.stdout.splitlines() if "pid = " in l]
        assert pid_line, f"no pid in launchctl print output:\n{r.stdout}"
        pid = pid_line[0].split("=")[1].strip()
        ps = subprocess.run(["ps", "-o", "user=", "-p", pid],
                            capture_output=True, text=True, timeout=5)
        assert ps.stdout.strip() == os.environ.get("USER"), \
            f"daemon running as {ps.stdout.strip()!r}, expected user {os.environ.get('USER')!r}"

        # --- Step 1b: LaunchDaemon socket activation binds :80 and :443 ---
        # Ship 3/4 gap: LaunchAgent socket activation returned unbound file
        # descriptors for the reverse-proxy ports. Ship 4.2's LaunchDaemon
        # makes them genuinely bound. Verify with a plain TCP connect —
        # ECONNREFUSED means the bind never happened.
        import socket
        for port in (80, 443):
            try:
                s = socket.create_connection(("127.0.0.1", port), timeout=5)
                s.close()
            except ConnectionRefusedError:
                pytest.fail(
                    f"nothing listening on 127.0.0.1:{port} — proxy not "
                    f"bound. Ship 3/4-era LaunchAgent socket-activation bug."
                )
            except OSError as e:
                pytest.fail(f"unexpected error connecting to :{port}: {e}")

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
        assert vm.state() == "running", (
            f"VM should be running after cold-start; got {vm.state()!r}"
        )
        result = vm.exec("true")
        assert result.exit_code == 0, (
            f"exec true in VM failed: exit={result.exit_code}, "
            f"stderr={result.stderr!r}"
        )

        # --- Step 4: teardown ---
        devm.teardown(yes=True, timeout=60)

        # Allow a brief settle for Tart to remove the VM.
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if vm.state() == "absent":
                break
            time.sleep(0.5)
        assert vm.state() == "absent", (
            f"VM still present after teardown; state={vm.state()!r}"
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

        assert not _LAUNCH_DAEMON_PLIST.exists(), \
            "LaunchDaemon plist still present after uninstall"

        # Runtime dir should be gone (socket, CA, etc.).
        assert not os.path.exists(_runtime_dir()), (
            "devm runtime dir still present after uninstall"
        )

        # Restore the daemon so downstream tests (which trust run.sh's
        # up-front install via the verify-only autouse fixture) still
        # see a matching daemon.
        subprocess.run(
            [devm.path, "install"], capture_output=True, timeout=780,
        )
