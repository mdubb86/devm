"""sbx-anchor 02: anchor alive + a `sbx exec -it` invocation whose
host process inherits the test driver's stdio (no PTY allocated) —
daemon still survives.

CAVEAT: `sbx exec -it` requires a real TTY on its host stdin. With
subprocess.Popen(stdin=PIPE) there's no TTY, so the user-shell
process exits IMMEDIATELY with "ERROR: the input device is not a
TTY". This test therefore does NOT exercise an actual interactive
shell — it exercises the case where a user-shell *attempt* fails
fast under the anchor. The point being verified is that the
daemon is unaffected: anchor-alive holds the daemon up on its own.

For a real interactive-shell exercise (production shape), see
test_sbx_anchor_07, which uses pexpect → wrapper → execvp to put
an actual `sbx exec -it bash` into the pexpect PTY via inheritance.
"""
from __future__ import annotations
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
def test_anchor_alive_inherited_user_shell_keeps_daemon(sandbox_name):
    kit = materialize_kit()
    anchor = bring_up_anchored(sandbox_name, kit)
    user_shell = None
    try:
        # User shell with INHERITED stdio = no fresh PTY allocated.
        # subprocess.Popen here intentionally mirrors Go exec.Cmd's
        # inherit-from-parent default. Using `pexpect.spawn` here would
        # be WRONG: pexpect allocates a fresh PTY for the child, which
        # bypasses the production shape entirely.
        user_shell = subprocess.Popen(
            ["sbx", "exec", "-it", sandbox_name, "bash"],
            stdin=subprocess.PIPE,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
        )

        # Give the user shell a moment to register its session.
        time.sleep(0.5)

        # Wait well past the 5s kill window.
        time.sleep(30)

        result = read_daemon_lifetime(sandbox_name)
        assert result is not None, "could not read daemon trail files"
        start, last, count, alive = result
        lifetime = last - start
        print(f"\n  start={start:.3f} last={last:.3f} count={count} "
              f"alive={alive} lifetime={lifetime:.2f}s\n", flush=True)

        assert alive, (
            "daemon not running. Anchor was alive throughout — the "
            "inherited-stdio user shell alone is killing the daemon. "
            "Anchor-stays-alive is not sufficient."
        )
        assert lifetime > 25, (
            f"daemon lived only {lifetime:.2f}s; expected >25s."
        )
    finally:
        if user_shell is not None:
            try:
                user_shell.stdin.close()
            except Exception:
                pass
            try:
                user_shell.wait(timeout=3)
            except Exception:
                user_shell.kill()
        if anchor.poll() is None:
            anchor.kill()
            try:
                anchor.wait(timeout=3)
            except Exception:
                pass
        sbx.sandbox_rm(sandbox_name)
        kit.cleanup()
