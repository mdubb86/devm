"""sbx-anchor 05: while the anchor is alive, `sbx ports --publish`
publishes a mapping that:
  - appears immediately in `sbx ports --json` (no phantom — i.e. the
    "publish appears to succeed then mapping vanishes" behavior that
    Quirk #3 documents post-anchor-kill does NOT manifest here)
  - persists for the duration of the test (no spontaneous vanish)

This is what removes the post-handoff publish gymnastics from devm:
under the new architecture we can publish whenever, in any order,
and not worry about ports tied to a dying anchor session.
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


SANDBOX_PORT = 8080
HOST_PORT = 50200  # arbitrary; just outside the ranges used by other tests


def _has_mapping(name: str, host: int, sandbox_port: int) -> bool:
    for m in sbx.ports(name):
        if m.get("host_port") == host and m.get("sandbox_port") == sandbox_port:
            return True
    return False


@pytest.mark.timeout(120)
def test_publish_sticks_while_anchor_alive(sandbox_name):
    kit = materialize_kit()
    anchor = bring_up_anchored(sandbox_name, kit)
    try:
        spec = f"{HOST_PORT}:{SANDBOX_PORT}"
        pub = subprocess.run(
            ["sbx", "ports", sandbox_name, "--publish", spec],
            capture_output=True, timeout=15,
        )
        assert pub.returncode == 0, (
            f"sbx ports --publish failed: stdout={pub.stdout!r} "
            f"stderr={pub.stderr!r}"
        )

        # Should appear immediately. We poll for up to a few seconds
        # because sbx's list visibility can lag briefly (the wait is
        # not the bug under test).
        deadline = time.monotonic() + 5
        appeared = False
        while time.monotonic() < deadline:
            if _has_mapping(sandbox_name, HOST_PORT, SANDBOX_PORT):
                appeared = True
                break
            time.sleep(0.1)
        assert appeared, (
            f"publish for {spec} never appeared in `sbx ports --json` "
            f"within 5s of publish returning success"
        )

        # Hold for 10s and re-verify — this is what catches "publish
        # succeeded then vanished" (Quirk #3 post-anchor-kill phantom
        # behavior would manifest as a disappearance within ~1s).
        time.sleep(10)
        assert _has_mapping(sandbox_name, HOST_PORT, SANDBOX_PORT), (
            f"publish for {spec} disappeared during the 10s observation "
            f"window. Anchor is alive (poll={anchor.poll()}); something "
            f"else is tearing the mapping down."
        )
    finally:
        # Best-effort unpublish; cleanup will tear down the sandbox anyway.
        subprocess.run(
            ["sbx", "ports", sandbox_name, "--unpublish", f"{HOST_PORT}:{SANDBOX_PORT}"],
            capture_output=True, timeout=10,
        )
        if anchor.poll() is None:
            anchor.kill()
            try:
                anchor.wait(timeout=3)
            except Exception:
                pass
        sbx.sandbox_rm(sandbox_name)
        kit.cleanup()
