"""51: a failing install step surfaces loud — non-zero exit, no zombie VM.

When any install step exits non-zero, `devm shell` MUST exit non-zero
and leave no half-created sandbox behind. A silently-broken sandbox
would defeat devm's loud-failure UX.

What this pins:
  - `devm shell` exits non-zero when install returns non-zero.
  - No VM is left behind after the failure (state == "absent").

What it doesn't cover (tested elsewhere):
  - Successful cold-start -> test_50.
  - Stop/teardown paths -> test_52, test_53.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.xfail(
    strict=False,
    reason=(
        "devm bug B: orchestrator/shell.go RunShell returns provision error without "
        "VM teardown, leaving a zombie VM. Remove xfail when bug B lands."
    ),
)
@pytest.mark.timeout(120)
def test_install_failure_surfaces_loud(devm, workspace):
    # Override the workspace config to use a failing install step.
    # `false` always exits 1.
    workspace.write_devmyaml(install=["false"])

    # Run devm shell -- true; expect non-zero (install failure).
    p = subprocess.run(
        [devm.path, "shell", "--", "true"],
        capture_output=True, cwd=str(workspace.path), timeout=120,
    )
    assert p.returncode != 0, (
        f"devm shell should exit non-zero when install returns non-zero; "
        f"got rc={p.returncode}\nstderr={p.stderr.decode()}"
    )
    # No zombie VM should remain.
    vm = TartSandbox(name=workspace.sandbox_name)
    assert vm.state() == "absent", (
        f"failed install must not leave a VM behind; "
        f"VM is still in state {vm.state()!r}"
    )
