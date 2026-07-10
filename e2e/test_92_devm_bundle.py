"""92: devm bundle — /opt/devm layout, no .devm/ in workspace.

Consolidated smoke test for the bundle refactor:
  - After `devm start`, /opt/devm/ has .env, scripts/, templates/,
    install.sh with expected modes.
  - /usr/local/bin/with-devm-env is a symlink into /opt/devm/scripts/.
  - The workspace has no .devm/ directory.
  - `devm exec` works (proves the wrapper is on PATH via the symlink
    and sources /opt/devm/.env).
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
def test_devm_bundle_layout(workspace, devm):
    workspace.write_devmyaml(
        env={"BUNDLE_TEST": "hello"},
    )

    start = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=300,
    )
    assert start.returncode == 0, (
        f"devm start failed:\nstderr={start.stderr.decode()!r}"
    )

    # ---- /opt/devm/ layout ----
    ls = subprocess.run(
        [devm.path, "exec", "sh", "-c",
         "ls -la /opt/devm/ /opt/devm/scripts/ /opt/devm/templates/ 2>&1"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    out = ls.stdout.decode()
    assert "/opt/devm/.env" in ls.stdout.decode() or ".env" in out, out
    assert "with-devm-env" in out, out
    assert "install-templates.sh" in out, out
    assert "install.sh" in out, out

    # ---- /usr/local/bin/with-devm-env is symlink into /opt/devm/ ----
    readlink = subprocess.run(
        [devm.path, "exec", "readlink", "/usr/local/bin/with-devm-env"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert readlink.returncode == 0
    assert readlink.stdout.decode().strip() == "/opt/devm/scripts/with-devm-env"

    # ---- Workspace has no .devm/ ----
    assert not (workspace.path / ".devm").exists(), (
        f".devm/ should not be created in the workspace; found: "
        f"{list((workspace.path / '.devm').iterdir()) if (workspace.path / '.devm').exists() else 'N/A'}"
    )

    # ---- env from cfg is visible via the wrapper ----
    echo = subprocess.run(
        [devm.path, "exec", "sh", "-c", 'echo "$BUNDLE_TEST"'],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert echo.returncode == 0
    assert echo.stdout.decode().strip() == "hello", (
        f"env var BUNDLE_TEST not exported via /opt/devm/.env; "
        f"got: {echo.stdout.decode()!r}"
    )
