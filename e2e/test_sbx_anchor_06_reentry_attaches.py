"""sbx-anchor 06: with the anchor alive, a user can `sbx exec -it bash`
the sandbox, exit, and re-attach again. The sandbox stays running
throughout — the anchor's session is enough to hold it independent of
whether a user shell is currently attached.

This is what makes devm's warm-path (`sb.IsRunning()` true → skip
anchor spawn, just attach) work: the anchor from an earlier `devm
shell` invocation is still holding the sandbox, so the new shell
just attaches.
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


def _attach_and_run_marker(sandbox_name: str, marker: str) -> None:
    """Run a single command via `sbx exec NAME bash -c "echo MARKER"`.
    We use the non-interactive form here: `sbx exec -it` requires a
    real TTY on stdin and fails under subprocess pipes. The point
    being tested is that the sandbox is **reachable** for an exec
    attempt while the anchor holds it — repeated reachability is the
    re-entry property. Interactive-shell roundtrip is covered
    separately in test_sbx_anchor_07."""
    r = subprocess.run(
        ["sbx", "exec", sandbox_name, "bash", "-c", f"echo {marker}"],
        capture_output=True, timeout=15,
    )
    assert r.returncode == 0, (
        f"sbx exec failed rc={r.returncode}; "
        f"stdout={r.stdout!r} stderr={r.stderr!r}"
    )
    assert marker.encode() in r.stdout, (
        f"marker {marker!r} not echoed back; stdout={r.stdout!r}"
    )


@pytest.mark.timeout(120)
def test_reentry_attaches_to_live_anchor(sandbox_name):
    kit = materialize_kit()
    anchor = bring_up_anchored(sandbox_name, kit)
    try:
        # Smoke check: anchor must be alive immediately after bring-up.
        assert anchor.poll() is None, (
            f"anchor died during bring-up (rc={anchor.poll()}); "
            f"bring_up_anchored returned despite this — wait_running/"
            f"wait_exec_ready don't check liveness"
        )

        # First user shell — attach, run marker, exit.
        _attach_and_run_marker(sandbox_name, "FIRST_SHELL")

        # Sandbox must still be running between attach + re-attach.
        assert sbx.sandbox_state(sandbox_name) == "running", (
            "sandbox stopped after the first user shell exited; anchor "
            "is not holding the session on its own"
        )
        assert anchor.poll() is None, "anchor died between sessions"

        # Wait a moment to make sure no delayed teardown is queued.
        time.sleep(2)
        assert sbx.sandbox_state(sandbox_name) == "running"

        # Second user shell — must succeed too.
        _attach_and_run_marker(sandbox_name, "SECOND_SHELL")

        assert sbx.sandbox_state(sandbox_name) == "running", (
            "sandbox stopped after the second user shell exited"
        )
        assert anchor.poll() is None, (
            "anchor died at some point during the re-entry sequence"
        )
    finally:
        if anchor.poll() is None:
            anchor.kill()
            try:
                anchor.wait(timeout=3)
            except Exception:
                pass
        sbx.sandbox_rm(sandbox_name)
        kit.cleanup()
