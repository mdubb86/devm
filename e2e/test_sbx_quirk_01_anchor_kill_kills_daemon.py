"""sbx-quirk 01: pins the upstream sbx 5s-daemon-kill behavior.

When the `sbx run` anchor is killed AND the user-shell host process
does not hold a fresh dedicated PTY (the production shape under
the OLD devm flow), sbx kills any process launched from
`commands.startup` approximately 5 seconds later.

This test exists as a *regression guard for upstream sbx*: if a
future sbx release fixes this behavior, the test fails LOUDLY so we
notice and can drop the workarounds documented in
`docs/sbx-quirks.md` quirk #5.

In the new (anchor-alive) architecture devm never kills the anchor
so this quirk no longer affects it — but the test is still
worthwhile because it pins WHEN upstream changes behavior.
"""
from __future__ import annotations
import os
import signal
import subprocess
import time

import pytest

from helpers import sbx
from helpers.sbx_kit import (
    bring_up_anchored,
    materialize_kit,
    read_daemon_lifetime,
)


@pytest.mark.timeout(120)
def test_anchor_kill_plus_no_pty_user_shell_kills_daemon(sandbox_name):
    """Kill the anchor + spawn a no-PTY user-shell attempt → daemon
    dies at ~5s. If this STARTS PASSING (daemon lives), upstream sbx
    fixed the kill timer."""
    kit = materialize_kit()
    anchor = bring_up_anchored(sandbox_name, kit)
    us = None
    try:
        # Bring up a no-PTY user-shell. (As documented in test 02, this
        # actually fails with "no TTY" immediately, but the host-side
        # process exists briefly — same shape as the OLD broken devm
        # flow that masked the bug for so long.)
        us = subprocess.Popen(
            ["sbx", "exec", "-it", sandbox_name, "bash"],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.STDOUT,
        )
        time.sleep(0.5)
        # Kill the anchor — this is the trigger for the 5s daemon kill.
        anchor.kill()
        try:
            anchor.wait(timeout=3)
        except Exception:
            pass

        # Wait well past 5s.
        time.sleep(15)

        result = read_daemon_lifetime(sandbox_name)
        assert result is not None, "could not read daemon trail"
        start, last, count, alive = result
        lifetime = last - start
        print(f"\n  start={start:.3f} last={last:.3f} count={count} "
              f"alive={alive} lifetime={lifetime:.2f}s\n", flush=True)

        # Quirk guard: this SHOULD die at ~5s. If sbx fixes it (daemon
        # stays alive past 10s), this assertion FAILS loudly and we
        # can drop the anchor-stays-alive workaround entirely.
        assert not alive, (
            f"daemon is still alive after anchor kill + no-PTY user "
            f"shell. sbx may have fixed the 5s kill timer — "
            f"update docs/sbx-quirks.md quirk #5 and consider "
            f"whether the anchor-alive architecture is still needed."
        )
        assert lifetime < 10, (
            f"daemon lived {lifetime:.2f}s before dying. The 5s kill "
            f"window may have expanded — investigate before relying "
            f"on quirk-based timing."
        )
    finally:
        if us is not None:
            try:
                us.stdin.close()
            except Exception:
                pass
            try:
                us.wait(timeout=3)
            except Exception:
                us.kill()
        if anchor.poll() is None:
            anchor.kill()
            try:
                anchor.wait(timeout=3)
            except Exception:
                pass
        sbx.sandbox_rm(sandbox_name)
        kit.cleanup()
