"""75: install: step that hangs longer than DEVM_INSTALL_STEP_TIMEOUT_S
makes devm shell exit non-zero with a structured timeout error.

Pin Task 4 from the e2e refresh: per-step timeout via
DEVM_INSTALL_STEP_TIMEOUT_S env var (default 600s).
"""
import os
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(60)
def test_install_step_exceeds_timeout(workspace, devm):
    # 1-second timeout, 5-second sleep. Step trips the deadline; devm
    # surfaces "install step <N> ("<cmd>") timed out after 1s".
    workspace.write_devmyaml(install=["sleep 5"])
    env = os.environ.copy()
    env["DEVM_INSTALL_STEP_TIMEOUT_S"] = "1"
    proc = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path),
        capture_output=True, timeout=30,
        env=env,
    )
    assert proc.returncode != 0, (
        f"devm shell should exit non-zero on install timeout; got rc=0\n"
        f"stderr={proc.stderr.decode()!r}"
    )
    err = proc.stderr.decode()
    assert "install step 1" in err
    assert "sleep 5" in err
    assert "timed out" in err
