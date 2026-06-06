"""tripwire: `sbx exec NAME ...` with piped stdin hangs.

`sbx exec NAME sh -c "cat > FILE"` with stdin piped from a Python
subprocess.Popen (mirrors Go's `exec.Cmd.Stdin = pipeReader`) does
NOT complete — the cat reader on the in-VM side never sees EOF and
the host-side Popen hangs.

devm's `WriteSnapshot` originally took this shape and hung
indefinitely. The workaround in `internal/orchestrator/snapshot.go`
sidesteps it by base64-encoding the content on the command line:
`sbx exec NAME sh -c "echo <b64> | base64 -d > FILE"`. No stdin pipe,
no hang.

Tripwire: this test runs the broken pattern with a short timeout. If
it completes within the timeout, sbx fixed the hang — and we can drop
the base64 workaround in snapshot.go.
"""
from __future__ import annotations
import subprocess
import time

import pytest

from helpers import sbx
from helpers.sbx_kit import (
    bring_up_anchored,
    materialize_kit,
)

pytestmark = pytest.mark.sbx_tripwire


@pytest.mark.timeout(120)
def test_sbx_exec_stdin_pipe_hangs(sandbox_name):
    """`sbx exec NAME sh -c "cat > FILE"` with piped stdin hangs.
    Quirk guard: passes today (the call hangs and we timeout-kill it);
    if it completes within the inner timeout, sbx fixed the bug."""
    kit = materialize_kit()
    anchor = bring_up_anchored(sandbox_name, kit)
    content = "hello-from-quirk-05\n"
    proc = None
    try:
        proc = subprocess.Popen(
            ["sbx", "exec", sandbox_name, "sh", "-c", "cat > /tmp/quirk5.out"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
        )
        # Write the content and close stdin to send EOF.
        try:
            stdout, stderr = proc.communicate(
                input=content.encode(), timeout=15,
            )
        except subprocess.TimeoutExpired:
            # Expected: the call hangs. This is the quirk.
            stdout = stderr = b""
            hung = True
        else:
            hung = False

        print(f"\n  hung={hung} stdout={stdout!r} stderr={stderr!r}\n",
              flush=True)

        if not hung:
            # Pipe completed within the inner timeout — check if the
            # file actually got the content. If yes, upstream is fixed.
            r = subprocess.run(
                ["sbx", "exec", sandbox_name, "cat", "/tmp/quirk5.out"],
                capture_output=True, timeout=10,
            )
            received = r.stdout.decode()
            assert received != content, (
                f"sbx exec with piped stdin completed AND delivered "
                f"content correctly. Upstream sbx fixed the stdin-pipe "
                f"hang — drop the base64 workaround in "
                f"internal/orchestrator/snapshot.go."
            )
            pytest.fail(
                f"call completed but content was wrong "
                f"(expected={content!r} got={received!r}); "
                f"investigate before declaring upstream fix."
            )
    finally:
        if proc is not None and proc.poll() is None:
            try:
                proc.kill()
                proc.wait(timeout=3)
            except Exception:
                pass
        if anchor.poll() is None:
            anchor.kill()
            try:
                anchor.wait(timeout=3)
            except Exception:
                pass
        sbx.sandbox_rm(sandbox_name)
        kit.cleanup()
