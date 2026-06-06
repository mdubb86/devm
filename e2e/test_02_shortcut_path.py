"""02: two concurrent devm shells share a running sandbox."""
import subprocess
import time

import pytest

from helpers import Shell, sbx

pytestmark = pytest.mark.devm


@pytest.mark.timeout(90)
def test_shortcut_path(workspace, devm, sandbox_name):
    workspace.write_devmyaml()

    with Shell(devm, cwd=str(workspace.path)) as first:
        first.expect_prompt(timeout=60)
        with Shell(devm, cwd=str(workspace.path)) as second:
            second.expect_prompt(timeout=60)
            # Both shells alive in the same sandbox: there should be
            # >= 2 bashes on pts/N inside the VM.
            out = subprocess.run(
                ["sbx", "exec", sandbox_name, "bash", "-c",
                 "ps -eo comm,tty | grep -c '^bash *pts/'"],
                capture_output=True, timeout=15, check=True,
            ).stdout.decode().strip()
            assert int(out) >= 2, f"expected >=2 pty bashes; got {out}"
            second.exit(timeout=30)
        first.exit(timeout=30)

    # Anchor-alive: explicitly stop after both shells exit.
    devm.stop(yes=True)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "stopped":
            return
        time.sleep(0.5)
    pytest.fail("sandbox never reached stopped")
