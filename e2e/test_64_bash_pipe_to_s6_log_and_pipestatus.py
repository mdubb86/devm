"""64: bash pipe to s6-log captures both streams; PIPESTATUS gives user rc.

Pins the two bash primitives the startup-supervision wrappers
(wrap-fg.sh, wrap-bg.sh) depend on:

  1. `cmd 2>&1 | s6-log <dir>` writes BOTH stdout and stderr from cmd
     into <dir>/current. Confirms fd-dup at exec time + the pipe to
     s6-log captures the merged stream end-to-end on the Tart base.

  2. `${PIPESTATUS[0]}` captures cmd's exit code (NOT s6-log's). This
     is load-bearing: the wrapper writes the user command's rc to the
     marker and uses it for fail-fast detection, so it must read cmd's
     status, not s6-log's.

Devm dependency: wrap-fg.sh uses exactly this pipe shape. If either
property breaks on this base image, stdout/stderr capture or rc
handling is broken for all supervised services.

The binary is always arm64 (Tart VMs are Apple Silicon). We stage the
embedded s6-log from internal/scripts/ via the workspace virtio-fs
mount so there's no apt dependency.
"""
from __future__ import annotations

import os
import shutil

import pytest

pytestmark = pytest.mark.devm


def _embedded_s6_log_path() -> str:
    here = os.path.dirname(os.path.abspath(__file__))
    repo_root = os.path.dirname(here)
    return os.path.join(repo_root, "internal", "scripts", "s6-log.linux-arm64")


@pytest.mark.timeout(180)
def test_pipe_to_s6_log_captures_both_streams_and_pipestatus_works(
    workspace, devm, tart_sandbox
):
    s6log_host = _embedded_s6_log_path()
    assert os.path.exists(s6log_host), (
        f"embedded s6-log.linux-arm64 not found at {s6log_host}"
    )

    # Stage the binary in the workspace so it's at the same path in VM.
    s6log_in_ws = workspace.path / "s6-log"
    shutil.copy(s6log_host, s6log_in_ws)
    s6log_in_ws.chmod(0o755)
    s6log_vm = str(s6log_in_ws)

    assert tart_sandbox.state() == "running", (
        f"expected VM running; got {tart_sandbox.state()!r}"
    )

    # 1. cmd 2>&1 | s6-log writes both streams into <dir>/current.
    #    Probe cmd emits to stdout AND stderr; both lines must end up
    #    in the logdir.
    r = tart_sandbox.exec_shell(
        f"rm -rf /tmp/probe-merge && mkdir -p /tmp/probe-merge && "
        f"bash -c '{{ echo OUTLINE; echo ERRLINE 1>&2; }} "
        f"2>&1 | {s6log_vm} -b n20 s1000000 T /tmp/probe-merge' && "
        f"cat /tmp/probe-merge/current"
    )
    assert r.ok, (
        f"basic 2>&1 | s6-log probe failed: stdout={r.stdout!r} stderr={r.stderr!r}"
    )
    assert "OUTLINE" in r.stdout, (
        f"stdout line missing from merged log; current:\n{r.stdout}"
    )
    assert "ERRLINE" in r.stdout, (
        f"stderr line missing from merged log (2>&1 didn't dup into pipe); "
        f"current:\n{r.stdout}"
    )

    # 2. PIPESTATUS[0] gives cmd's rc, not s6-log's.
    #    Failing user cmd (exits 7), successful s6-log. PIPESTATUS[0]
    #    must be 7, even though s6-log succeeded (0).
    r = tart_sandbox.exec_shell(
        f"rm -rf /tmp/probe-rc && mkdir -p /tmp/probe-rc && "
        f"bash -c 'set -o pipefail; "
        f"( echo line; exit 7 ) 2>&1 | {s6log_vm} -b n20 s1000000 T /tmp/probe-rc; "
        f"echo got=${{PIPESTATUS[0]}}'"
    )
    # Outer rc is from `echo got=...`, which succeeds.
    assert r.ok, (
        f"PIPESTATUS probe outer command failed: stderr={r.stderr!r}"
    )
    assert "got=7" in r.stdout, (
        f"PIPESTATUS[0] did not capture user cmd's rc=7; got: {r.stdout!r}. "
        f"The wrapper relies on PIPESTATUS[0] to write the marker rc; "
        f"if this shows the wrong rc, fail-fast semantics break."
    )
