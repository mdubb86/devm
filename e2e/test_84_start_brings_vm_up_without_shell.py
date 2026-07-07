"""84: `devm start` brings the VM to running and returns immediately.

`devm shell -- true` already does this — cold-start, exit — but `devm
start` is a clearer intent for scripts / CI / "warm the VM up in the
background before I attach later". This test pins:

  - `devm start` cold-starts a stopped/absent VM and returns 0.
  - The VM ends up in the 'running' state.
  - No interactive shell was attached (the process returns even without
    a TTY, without needing bash to exit).
  - The VM STAYS running after start returns (anchor-alive, same
    behavior as `devm shell -- true`).

What it doesn't cover (tested elsewhere):
  - Cold-start via `devm shell` -> test_01, test_50.
  - Anchor-alive after interactive shell exit -> test_01.
  - devm stop / teardown lifecycle -> test_03, test_05, test_52.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_start_brings_vm_up_without_shell(workspace, devm, sandbox_name):
    workspace.write_devmyaml()

    sandbox = TartSandbox(name=sandbox_name)
    # Precondition: no VM exists yet.
    pre_state = sandbox.state()
    assert pre_state == "absent", (
        f"expected VM absent before `devm start`; got {pre_state!r}"
    )

    # `devm start` — no `--` needed, no command argument. Returns after
    # cold-start completes. Capture output for debugging on failure.
    p = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=180,
    )
    assert p.returncode == 0, (
        f"devm start should exit 0 on successful cold-start; got rc={p.returncode}\n"
        f"stdout={p.stdout.decode()!r}\nstderr={p.stderr.decode()!r}"
    )

    # VM should be running.
    current = sandbox.state()
    assert current == "running", (
        f"expected VM running after `devm start`; got {current!r}"
    )


@pytest.mark.timeout(120)
def test_start_rejects_extra_args(workspace, devm):
    """`devm start` takes no positional args. Passing any should fail
    with a clear error (not silently ignore them)."""
    workspace.write_devmyaml()
    p = subprocess.run(
        [devm.path, "start", "some-extra-arg"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=30,
    )
    assert p.returncode != 0, (
        "devm start should reject extra arguments"
    )
    combined = (p.stdout + p.stderr).decode()
    assert "no arguments" in combined or "takes no" in combined, (
        f"error message should say start takes no args; got: {combined!r}"
    )
