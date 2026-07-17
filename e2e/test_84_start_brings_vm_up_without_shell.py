"""84: `devm start` brings the VM up; chained `devm exec`/`devm stop` lifecycle.

`devm shell -- true` already cold-starts a VM — but `devm start` is a
clearer intent for scripts / CI / "warm the VM up in the background
before I attach later", and `devm exec` is the one-shot equivalent of
`docker exec`. Consolidated (was also test_85's two VM-needing
subtests and test_86's stopped-VM variant): one boot chains `devm
start` -> `devm exec` (one-shot + auto-cd + flag passthrough) -> `devm
stop` -> `devm exec` failing loud on the now-stopped VM. Each of these
previously cold-started its own VM to prove one leg of the same
start/exec/stop lifecycle.

What this pins:
  - `devm start` cold-starts an absent VM and returns 0.
  - The VM ends up 'running'; no interactive shell was attached (the
    process returns even without a TTY, without needing bash to exit).
  - The VM STAYS running after start returns (anchor-alive, same
    behavior as `devm shell -- true`).
  - `devm exec pwd` prints the workspace path (auto-cd to $WORKSPACE,
    the with-devm-env wrapper's contract — was test_85).
  - `devm exec ls -la /` passes flags through to the target command
    (DisableFlagParsing — was test_85).
  - `devm stop --yes` transitions running -> stopped.
  - `devm exec` on the now-stopped VM fails loud with "not running",
    never silently cold-starting (was test_86's stopped-VM variant).

What it doesn't cover (tested elsewhere):
  - Cold-start via `devm shell` -> test_01, test_50.
  - Anchor-alive after interactive shell exit -> test_01.
  - devm stop / teardown lifecycle -> test_03, test_05, test_52.
  - `devm exec` with no argv (usage error, no VM needed) -> test_85.
  - `devm exec` on a never-provisioned (absent) VM -> test_86.
  - Exit code propagation via shell -> test_50.
  - Env injection into wrapper -> test_26.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_start_then_exec_then_stop_fails_exec(workspace, devm, sandbox_name):
    workspace.write_devmyaml()

    sandbox = TartSandbox(name=sandbox_name)
    # Precondition: no VM exists yet.
    pre_state = sandbox.state()
    assert pre_state == "absent", (
        f"expected VM absent before `devm start`; got {pre_state!r}"
    )

    # ---- devm start: cold-starts, returns 0, no shell attached, VM
    # ---- stays up (anchor-alive). ----
    start = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=180,
    )
    assert start.returncode == 0, (
        f"devm start should exit 0 on successful cold-start; got rc={start.returncode}\n"
        f"stdout={start.stdout.decode()!r}\nstderr={start.stderr.decode()!r}"
    )
    current = sandbox.state()
    assert current == "running", (
        f"expected VM running after `devm start`; got {current!r}"
    )

    # ---- devm exec pwd: one-shot, lands in $WORKSPACE (was test_85). ----
    p = subprocess.run(
        [devm.path, "exec", "pwd"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=60,
    )
    assert p.returncode == 0, (
        f"devm exec pwd failed: rc={p.returncode}\n"
        f"stdout={p.stdout.decode()!r}\nstderr={p.stderr.decode()!r}"
    )
    # stdout should be JUST pwd's output — provisioner diagnostic noise
    # goes to stderr. Take the last non-empty line to be tolerant of any
    # trailing whitespace / echo lines a caller might have set.
    lines = [ln for ln in p.stdout.decode().splitlines() if ln.strip()]
    got = lines[-1] if lines else ""
    # Workspace mount uses mirrored paths — same absolute path on host
    # and guest. Compare against the host path (which is what
    # workspace.path resolves to).
    assert got == str(workspace.path), (
        f"expected `pwd` to print workspace path {str(workspace.path)!r}; "
        f"got stdout={p.stdout.decode()!r} stderr={p.stderr.decode()!r}"
    )

    # ---- devm exec ls -la /: DisableFlagParsing passes flags through
    # ---- to the target command, not to devm (was test_85). ----
    p = subprocess.run(
        [devm.path, "exec", "ls", "-la", "/"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=60,
    )
    assert p.returncode == 0, (
        f"devm exec ls -la / failed: rc={p.returncode}\n"
        f"stdout={p.stdout.decode()!r}\nstderr={p.stderr.decode()!r}"
    )
    # -la output should include /etc, /usr, /home entries (basic Linux
    # root sanity — anything is fine, we just want to see -la was
    # honored, meaning it produced the long-listing header 'total N').
    out = p.stdout.decode()
    assert "total " in out, (
        f"ls -la output should start with 'total N' line; got:\n{out}"
    )

    # ---- devm stop --yes: running -> stopped. ----
    stop = subprocess.run(
        [devm.path, "stop", "--yes"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=60,
    )
    assert stop.returncode == 0, (
        f"devm stop failed: rc={stop.returncode}\n"
        f"stderr={stop.stderr.decode()!r}"
    )

    # ---- devm exec on the now-stopped VM fails loud, never silently
    # ---- cold-starting (was test_86's stopped-VM variant). ----
    p = subprocess.run(
        [devm.path, "exec", "true"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=30,
    )
    assert p.returncode != 0, (
        f"devm exec on stopped VM should fail; got rc=0\n"
        f"stdout={p.stdout.decode()!r}\nstderr={p.stderr.decode()!r}"
    )
    err = p.stderr.decode()
    assert "not running" in err, (
        f"expected 'not running' in error; got:\n{err}"
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
