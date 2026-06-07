"""29: a hung install: step surfaces a gate-timeout error.

Uses DEVM_INSTALL_GATE_TIMEOUT_S=15 (test hook documented in the
supervision design) to keep the test fast. With install:[sleep 200]
the sentinel never appears within 15s; devm surfaces the hung-step
error from readPhaseFailure (no .rc for the step, .ok absent).
"""
import os
import subprocess

import pytest

from helpers import Shell

pytestmark = pytest.mark.devm


@pytest.mark.timeout(60)
def test_install_hang_surfaces_gate_timeout(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        install=["sleep 200"],
    )

    env = os.environ.copy()
    env["DEVM_INSTALL_GATE_TIMEOUT_S"] = "15"

    proc = subprocess.run(
        [devm.path, "shell"],
        cwd=str(workspace.path),
        capture_output=True, timeout=45,
        env=env,
    )
    assert proc.returncode != 0, (
        f"devm shell should exit non-zero on install hang; got rc=0\n"
        f"stderr={proc.stderr.decode()!r}"
    )
    err = proc.stderr.decode()
    assert "install did not complete" in err, (
        f"expected 'install did not complete' in stderr; got:\n{err}"
    )
    assert "still running or hung" in err, (
        f"expected 'still running or hung' qualification; got:\n{err}"
    )
