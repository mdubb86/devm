"""30: per-step stdout/stderr is captured into /tmp/.devm/<phase>-<N>/current.

After a passing bringup, the supervision wrappers should have
written each step's combined output to the per-step logdir's
`current` file. Pin that the data is actually there and visible
via sbx exec cat.
"""
import subprocess

import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(120)
def test_install_and_startup_capture_visible(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        install=[
            "echo install-step-2-stdout",
            "echo install-step-3-stderr 1>&2",
        ],
        services={
            "api": {
                "port": 8080,
                "startup": [
                    {"command": ["sh", "-c", "echo startup-user-stdout"]},
                ],
            },
        },
    )

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=120)

        # Install step 2 (user install[0]) — its stdout is in current.
        r = subprocess.run(
            ["sbx", "exec", sandbox_name, "cat", "/tmp/.devm-install/install-2/current"],
            capture_output=True, timeout=10, check=True,
        ).stdout.decode()
        assert "install-step-2-stdout" in r, (
            f"expected captured stdout in install-2/current; got:\n{r}"
        )

        # Install step 3 (user install[1]) — its stderr is in current
        # (both streams merge via 2>&1).
        r = subprocess.run(
            ["sbx", "exec", sandbox_name, "cat", "/tmp/.devm-install/install-3/current"],
            capture_output=True, timeout=10, check=True,
        ).stdout.decode()
        assert "install-step-3-stderr" in r, (
            f"expected captured stderr in install-3/current; got:\n{r}"
        )

        # Startup step 3 (first user startup) — its stdout is in current.
        # Note: cleanup=0, init-volumes=1, install-templates=2, user=3.
        r = subprocess.run(
            ["sbx", "exec", sandbox_name, "cat", "/tmp/.devm-startup/startup-3/current"],
            capture_output=True, timeout=10, check=True,
        ).stdout.decode()
        assert "startup-user-stdout" in r, (
            f"expected captured stdout in startup-3/current; got:\n{r}"
        )

        sh.exit(timeout=30)

    stop_and_wait_stopped(devm, sandbox_name)
