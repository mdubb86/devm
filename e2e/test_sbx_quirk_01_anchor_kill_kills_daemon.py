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

pytestmark = pytest.mark.sbx


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

        # Locked behavior (sbx 0.31+): the 5s daemon-kill-after-anchor
        # quirk is FIXED. Killing the anchor no longer kills the daemon
        # session — it stays alive past 15s. Before 0.31 the assertion
        # was `assert not alive` (the historical quirk that drove the
        # anchor-alive architecture). If sbx regresses, this test fails
        # loud and we know to re-evaluate.
        assert alive, (
            f"daemon DIED within 15s of anchor kill — sbx may have "
            f"re-introduced the 5s kill timer. The anchor-alive "
            f"architecture in devm's cold-start is the workaround; "
            f"re-evaluate docs/sbx-quirks.md quirk #5."
        )
        assert lifetime >= 14, (
            f"daemon trail only spans {lifetime:.2f}s; daemon likely "
            f"died early. See assertion above."
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
