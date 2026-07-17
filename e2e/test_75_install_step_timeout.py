"""75: install: step that hangs longer than DEVM_INSTALL_STEP_TIMEOUT_S
makes devm shell exit non-zero, and the failure surfaces as the composed
script's "install" provision stage.

Pin Task 4 from the e2e refresh: install:/startup: step timeout via the
DEVM_INSTALL_STEP_TIMEOUT_S env var (default 600s), now enforced by the
composed provisioning script's `timeout %d` wrapping (render.
RenderProvisionScript / provision.Provisioner) instead of the old
per-step Go provisioner.
"""
import os
import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(60)
def test_install_step_exceeds_timeout(workspace, devm):
    # 1-second timeout, 5-second sleep. `timeout 1` inside the composed
    # script kills the install: command with exit 124; `set -eo pipefail`
    # aborts the script right there, so the failure is classified at the
    # "install" stage. subprocess.run's own timeout (30s) is a correctness
    # backstop, not just a safety net: if DEVM_INSTALL_STEP_TIMEOUT_S were
    # silently ignored (the regression this test pins), `sleep 5` would
    # simply finish and `devm shell` would exit 0 well inside that budget —
    # this test would then fail on the returncode assertion below, not hang.
    workspace.write_devmyaml(install=["sleep 5"])
    env = os.environ.copy()
    env["DEVM_INSTALL_STEP_TIMEOUT_S"] = "1"
    start = time.monotonic()
    proc = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path),
        capture_output=True, timeout=30,
        env=env,
    )
    elapsed = time.monotonic() - start
    assert proc.returncode != 0, (
        f"devm shell should exit non-zero on install timeout; got rc=0\n"
        f"stderr={proc.stderr.decode()!r}"
    )
    err = proc.stderr.decode()
    # Failure is classified at the "install" provision stage (composed-
    # script model: provision.StepFailure's stage name, not the old
    # per-step `[step: <name>]` model).
    assert 'provision stage "install"' in err, (
        f"expected 'provision stage \"install\"' in stderr; got:\n{err}"
    )
    # `timeout 1` kills the step with exit 124, and the script propagates
    # that exit code — proves the 1s override actually fired, not some
    # unrelated install failure.
    assert "124" in err, (
        f"expected the timeout-killed exit code 124 in stderr; got:\n{err}"
    )
    # Genuinely fast: proves the 1s override bounded this run, not the
    # 600s default (which would either hang past subprocess.run's own
    # timeout, or — since `sleep 5` finishes on its own — succeed with
    # rc=0 and never reach here).
    assert elapsed < 25, (
        f"install timeout should fire fast under the 1s override; took {elapsed:.1f}s"
    )
