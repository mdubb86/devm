"""85: `devm exec` runs one-shot commands; auto-cd lands in $WORKSPACE.

Two related invariants:

1. `devm exec COMMAND [ARGS...]` runs a non-interactive command inside
   a *running* sandbox, captures its output, and returns its exit code.
   It does NOT cold-start — if the VM is stopped, exec fails loud (the
   docker exec / kubectl exec convention). See test_86 for the
   stopped-VM failure pin.

2. All wrapper-mediated invocations (shell, exec, and provisioner
   install: steps) auto-cd to $WORKSPACE before exec'ing argv. Same
   mental model as `docker run -w` — the workspace is your project
   root, and that's where a dev command expects to land.

What this pins:
  - `devm exec pwd` prints the workspace path (mirrored: same as host).
  - `devm exec` with no argv is a usage error.
  - Extra positional args after the command are passed through
    verbatim (proves DisableFlagParsing: e.g. `devm exec ls -la`).

What it doesn't cover (tested elsewhere):
  - `devm start` cold-start-and-return -> test_84.
  - `devm exec` fails when VM stopped -> test_86.
  - Exit code propagation via shell -> test_55.
  - Env injection into wrapper -> test_26.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(240)
def test_exec_pwd_lands_in_workspace(workspace, devm):
    """`devm exec pwd` returns the workspace path — the with-devm-env
    wrapper's auto-cd sets cwd to $WORKSPACE before running argv."""
    workspace.write_devmyaml()

    # Prime the sandbox — exec requires a running VM.
    start = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=180,
    )
    assert start.returncode == 0, (
        f"devm start failed: rc={start.returncode}\n"
        f"stderr={start.stderr.decode()!r}"
    )

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


@pytest.mark.timeout(60)
def test_exec_requires_command(workspace, devm):
    """`devm exec` with no positional args must fail loud, not open
    a shell or run a default. This is a cobra-level Args check that
    fires before we hit the running-VM gate — so no VM needed."""
    workspace.write_devmyaml()

    p = subprocess.run(
        [devm.path, "exec"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=30,
    )
    assert p.returncode != 0, (
        "devm exec with no command should fail"
    )


@pytest.mark.timeout(240)
def test_exec_passes_flags_to_target_command(workspace, devm):
    """DisableFlagParsing on execCmd means flags in argv go to the
    target command, not to devm. `devm exec ls -la /` must run
    `ls -la /` inside the VM, not error 'unknown flag: -la'."""
    workspace.write_devmyaml()

    start = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=180,
    )
    assert start.returncode == 0, (
        f"devm start failed: rc={start.returncode}\n"
        f"stderr={start.stderr.decode()!r}"
    )

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
