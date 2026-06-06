"""sbx-anchor 01: with the anchor (`sbx run`) left ALIVE and no user
shell attached at all, a background daemon launched from
`commands.startup` (foreground step + shell-level `nohup ... &`)
survives indefinitely.

This is the load-bearing assumption of the "anchor stays alive"
architecture: when we stop killing the anchor, the 5s daemon-kill
behavior we documented in docs/sbx-quirks.md quirk #5 simply does not
trigger. There is no user shell in this test — only the anchor + the
sandbox.

If this test fails, the proposed architecture is unsound and we need
to rethink before refactoring devm.
"""
from __future__ import annotations
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
def test_anchor_alive_no_user_shell_keeps_daemon(sandbox_name):
    kit = materialize_kit()
    anchor = bring_up_anchored(sandbox_name, kit)
    try:
        # Wait well past the 5s kill window that anchor-death would trigger.
        time.sleep(30)

        result = read_daemon_lifetime(sandbox_name)
        assert result is not None, "could not read daemon trail files"
        start, last, count, alive = result
        lifetime = last - start
        print(f"\n  start={start:.3f} last={last:.3f} count={count} "
              f"alive={alive} lifetime={lifetime:.2f}s\n", flush=True)

        assert alive, "daemon process not running"
        assert lifetime > 25, (
            f"daemon lived only {lifetime:.2f}s with anchor still alive; "
            f"expected >25s. This invalidates the anchor-stays-alive premise."
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
