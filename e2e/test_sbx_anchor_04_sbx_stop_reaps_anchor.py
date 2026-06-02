"""sbx-anchor 04: `sbx stop NAME` causes a long-running `sbx run`
anchor to exit cleanly within seconds. No PID file, no `pgrep`, no
manual `kill` — sbx handles cross-invocation cleanup on its own when
we ask it to stop.

This is what removes the need for a PID file / process-scan in the
new devm orchestrator: `devm stop` and `devm teardown` can just call
`sbx stop` and let the anchor get reaped as a side effect.

If this fails, devm needs explicit anchor tracking (pidfile or
pgrep-by-kit-path) as a fallback, and `devm stop` becomes a two-step
operation.
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


@pytest.mark.timeout(120)
def test_sbx_stop_reaps_anchor(sandbox_name):
    kit = materialize_kit()
    anchor = bring_up_anchored(sandbox_name, kit)
    try:
        # Anchor is alive and holding the sandbox session.
        assert anchor.poll() is None, "anchor exited before sbx stop ran"
        assert sbx.sandbox_state(sandbox_name) == "running"

        # Ask sbx to stop the sandbox. Anchor should follow.
        stop = subprocess.run(
            ["sbx", "stop", sandbox_name],
            capture_output=True, timeout=30,
        )
        assert stop.returncode == 0, (
            f"sbx stop failed: stdout={stop.stdout!r} stderr={stop.stderr!r}"
        )

        # Wait up to 10s for the anchor to exit on its own.
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if anchor.poll() is not None:
                break
            time.sleep(0.1)
        assert anchor.poll() is not None, (
            "anchor still alive 10s after sbx stop returned; sbx does NOT "
            "reap blocking `sbx run` processes when its sandbox is stopped"
        )

        # And the sandbox itself should be stopped.
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if sbx.sandbox_state(sandbox_name) == "stopped":
                break
            time.sleep(0.25)
        assert sbx.sandbox_state(sandbox_name) == "stopped"
    finally:
        if anchor.poll() is None:
            anchor.kill()
            try:
                anchor.wait(timeout=3)
            except Exception:
                pass
        sbx.sandbox_rm(sandbox_name)
        kit.cleanup()
