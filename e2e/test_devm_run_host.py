"""devm run <fn>: project devm.sh functions run on the Mac.

CLI-direct — no VM is created. Proves the recipe's promises:

  A. $DEVM_SIDE=host is set inside the function.
  B. $WORKSPACE equals the Mac project root (the dir containing devm.yaml).
  C. Positional args reach the function as $1, $2, ...
  D. The function's exit code becomes devm run's exit code.
  E. An unknown function errors non-zero and lists the available set.
  F. A project with no devm.sh errors non-zero with "no devm.sh".
  G. Invoked from a subdirectory, the walk-up finds the project root
     and `pwd` inside the function is that root.
  H. A syntax error in devm.sh errors non-zero at source time, not
     with a confusing "function not found".
  I. Ctrl-C (SIGINT) during a long-running function is forwarded; the
     child dies promptly AND surfaces as exit 130 (128 + SIGINT).
  J. Dash-prefixed positional args (--flag) reach the function as $1
     rather than being eaten by bash's own option parser.
  K. `devm run --help` shows usage without erroring as "function not
     found".

No daemon call, no sandbox spin-up — the only requirement is a bash on
PATH. Runs fast; kept under the `devm` marker (default suite).
"""
from __future__ import annotations

import os
import signal
import subprocess
import textwrap
import time
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


def _write_project(workspace, devm_sh: str) -> None:
    """Write a minimal devm.yaml + a devm.sh body into the workspace."""
    workspace.devm_yaml_path.write_text(textwrap.dedent(f"""\
        project:
          name: {workspace.vm_name}
    """))
    workspace.write_devm_sh(devm_sh)


@pytest.mark.timeout(60)
def test_devm_run_host(devm, workspace, sandbox_name):
    # Every assertion below shares this devm.sh — one project, many fn.
    _write_project(workspace, textwrap.dedent("""\
        #!/usr/bin/env bash
        set -eo pipefail

        show_side() {
          echo "SIDE=${DEVM_SIDE}"
          echo "WS=${WORKSPACE}"
          echo "PWD=$(pwd)"
        }

        echo_args() {
          echo "ARGS=$1 $2 $3"
        }

        exit_with() {
          exit "$1"
        }

        sleeper() {
          # Long enough that a SIGINT in the test kills it early.
          sleep 30
        }
    """))

    # A, B. DEVM_SIDE=host + WORKSPACE = Mac project root.
    r = subprocess.run(
        [devm.path, "run", "show_side"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=15,
    )
    assert r.returncode == 0, (
        f"show_side failed:\nstdout:\n{r.stdout.decode()}\n"
        f"stderr:\n{r.stderr.decode()}"
    )
    out = r.stdout.decode()
    assert "SIDE=host" in out, f"expected SIDE=host; got {out!r}"
    assert f"WS={workspace.path.resolve()}" in out, (
        f"expected WS={workspace.path.resolve()}; got {out!r}"
    )
    # G. cwd inside the function is the project root, not the caller's
    # cwd (same as B above because test's cwd IS the project root here —
    # subdir case below covers the walk-up).
    assert f"PWD={workspace.path.resolve()}" in out

    # C. Positional args pass as $1, $2, $3.
    r = subprocess.run(
        [devm.path, "run", "echo_args", "alpha", "beta", "gamma"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=15,
    )
    assert r.returncode == 0, r.stderr.decode()
    assert b"ARGS=alpha beta gamma" in r.stdout, r.stdout

    # J. Dash-prefixed args — bash's option parser would eat `--flag`
    # without a `--` separator ahead of fnArgs. This asserts the
    # separator is there.
    r = subprocess.run(
        [devm.path, "run", "echo_args", "--flag", "-x", "end"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=15,
    )
    assert r.returncode == 0, (
        f"dash-prefixed args failed:\nstdout:\n{r.stdout.decode()}\n"
        f"stderr:\n{r.stderr.decode()}"
    )
    assert b"ARGS=--flag -x end" in r.stdout, r.stdout

    # D. Exit code passthrough.
    r = subprocess.run(
        [devm.path, "run", "exit_with", "7"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=15,
    )
    assert r.returncode == 7, (
        f"expected exit 7; got {r.returncode}\nstderr:\n{r.stderr.decode()}"
    )

    # E. Unknown function — non-zero, lists available.
    r = subprocess.run(
        [devm.path, "run", "nope"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=15,
    )
    assert r.returncode != 0
    err = r.stderr.decode()
    assert 'function "nope" not found' in err, err
    for fn in ("show_side", "echo_args", "exit_with", "sleeper"):
        assert fn in err, f"available set missing {fn}: {err!r}"

    # G. Subdirectory walk-up.
    subdir = workspace.path / "pkg" / "inner"
    subdir.mkdir(parents=True)
    r = subprocess.run(
        [devm.path, "run", "show_side"],
        cwd=str(subdir),
        capture_output=True,
        timeout=15,
    )
    assert r.returncode == 0, r.stderr.decode()
    assert f"WS={workspace.path.resolve()}" in r.stdout.decode()
    assert f"PWD={workspace.path.resolve()}" in r.stdout.decode()

    # H. Syntax error in devm.sh → non-zero, not "function not found".
    workspace.write_devm_sh("broken() {\n  echo start\n")  # missing closing }
    r = subprocess.run(
        [devm.path, "run", "broken"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=15,
    )
    assert r.returncode != 0
    # bash reports the parse error; the exact message varies by bash
    # version, but the key signal is that it's NOT our "function X not
    # found" (that would mean our pre-wrapper validation hid the real
    # issue — bash itself should surface the syntax error).
    err = r.stderr.decode()
    assert 'function "broken" not found' not in err, err

    # Restore a valid devm.sh for the SIGINT test.
    _write_project(workspace, textwrap.dedent("""\
        #!/usr/bin/env bash
        set -eo pipefail

        sleeper() { sleep 30; }
    """))

    # F. No devm.sh at all → non-zero with "no devm.sh".
    (workspace.path / "devm.sh").unlink()
    r = subprocess.run(
        [devm.path, "run", "sleeper"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=15,
    )
    assert r.returncode != 0
    assert b"no devm.sh" in r.stderr, r.stderr

    # I. Ctrl-C forwarded. Restore devm.sh with sleeper + send SIGINT.
    workspace.write_devm_sh(textwrap.dedent("""\
        #!/usr/bin/env bash
        set -eo pipefail
        sleeper() { sleep 30; }
    """))
    p = subprocess.Popen(
        [devm.path, "run", "sleeper"],
        cwd=str(workspace.path),
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    try:
        # Give bash a moment to start the sleep.
        time.sleep(0.5)
        p.send_signal(signal.SIGINT)
        try:
            rc = p.wait(timeout=5)
        except subprocess.TimeoutExpired:
            p.kill()
            pytest.fail(
                "devm run did not die within 5s of SIGINT — signal "
                "forwarding broken; the bash child kept sleeping."
            )
        # bash dying from SIGINT exits 130 (128 + 2). Anything else —
        # 1, 255 — means devm run flattened the signal-death code
        # instead of propagating it. Shells (|| true, trap handlers,
        # CI scripts) rely on the specific 130.
        assert rc == 130, (
            f"expected exit 130 (SIGINT, 128+2); got {rc}. "
            f"If 255, exitCodeFromExitError regressed on signal death."
        )
    finally:
        if p.poll() is None:
            p.kill()
            p.wait(timeout=2)

    # K. --help/-h must show cobra's usage, not "function not found".
    # DisableFlagParsing would otherwise route --help as the function
    # name (args[0]).
    for flag in ("--help", "-h"):
        r = subprocess.run(
            [devm.path, "run", flag],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=15,
        )
        assert r.returncode == 0, (
            f"devm run {flag} failed:\nstdout:\n{r.stdout.decode()}\n"
            f"stderr:\n{r.stderr.decode()}"
        )
        out = r.stdout.decode() + r.stderr.decode()
        assert "not found" not in out, (
            f"devm run {flag} tried to resolve a function: {out!r}"
        )
        assert "devm.sh" in out.lower(), (
            f"devm run {flag} did not surface help text: {out!r}"
        )
