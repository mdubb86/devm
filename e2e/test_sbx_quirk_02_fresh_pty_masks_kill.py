"""sbx-quirk 02: pins the "fresh-PTY user-shell masks the 5s daemon
kill" behavior.

If the host-side user-shell process holds a freshly-allocated PTY at
stdin (the shape `pexpect.spawn('sbx', ['exec', '-it', ...])`
produces because pexpect calls `pty.fork()` for its child), then
killing the anchor does NOT kill the daemon — sbx spares it for
reasons we don't fully understand.

This is the behavior that misled us early in this investigation:
sbx-13 used `pexpect.spawn(['sbx', 'exec', ...])` directly and
asserted "daemon survives anchor kill," which it does — but that
shape doesn't reflect what devm produces in production (devm's Go
exec.Cmd inherits the user terminal's PTY rather than allocating a
fresh one). The fresh-PTY case was the false-positive that masked
the actual production breakage for months.

This test pins the masking behavior so if upstream sbx changes it
(either by also killing fresh-PTY daemons OR by sparing inherited-
PTY daemons), the test fails and we learn.
"""
from __future__ import annotations
import time

import pexpect
import pytest

from helpers import sbx
from helpers.sbx_kit import (
    bring_up_anchored,
    materialize_kit,
    read_daemon_lifetime,
)


@pytest.mark.timeout(120)
def test_fresh_pty_user_shell_spares_daemon_after_anchor_kill(sandbox_name):
    """Anchor kill + pexpect-spawn (fresh-PTY) user shell → daemon
    SURVIVES. Pinned as a quirk because this masks the bug under the
    OLD devm flow; staying-alive is the unusual sbx behavior here."""
    kit = materialize_kit()
    anchor = bring_up_anchored(sandbox_name, kit)
    us = None
    try:
        # Fresh PTY allocated by pexpect (via pty.fork()).
        us = pexpect.spawn(
            "sbx", ["exec", "-it", sandbox_name, "bash"],
            encoding="utf-8", timeout=30, dimensions=(40, 200),
        )
        us.expect(r"\$ ?", timeout=30)
        time.sleep(0.5)

        # Kill the anchor — under inherited-PTY, this would kill the
        # daemon at 5s (quirk #1). Under fresh-PTY it does not.
        anchor.kill()
        try:
            anchor.wait(timeout=3)
        except Exception:
            pass

        # Wait well past the 5s death window.
        time.sleep(30)

        result = read_daemon_lifetime(sandbox_name)
        assert result is not None, "could not read daemon trail"
        start, last, count, alive = result
        lifetime = last - start
        print(f"\n  start={start:.3f} last={last:.3f} count={count} "
              f"alive={alive} lifetime={lifetime:.2f}s\n", flush=True)

        # Quirk: daemon SHOULD survive 30s under the fresh-PTY mask.
        assert alive, (
            "daemon died despite fresh-PTY user shell. sbx may have "
            "tightened the kill behavior; the fresh-PTY workaround "
            "no longer masks the issue."
        )
        assert lifetime > 25, (
            f"daemon lived only {lifetime:.2f}s under fresh-PTY mask. "
            f"Expected >25s."
        )
    finally:
        if us is not None:
            try:
                us.close(force=True)
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
