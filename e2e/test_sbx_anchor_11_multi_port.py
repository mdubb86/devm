"""sbx-anchor 11: multiple distinct port mappings published under
a live anchor — all appear, all persist, each independently
unpublishable.

Pins that sbx's port model is per-mapping, not per-publish-batch:
publishing port B doesn't disturb port A, unpublishing one of three
leaves the other two visible.
"""
from __future__ import annotations
import subprocess
import time

import pytest

from helpers import sbx
from helpers.sbx_kit import bring_up_anchored, materialize_kit


MAPPINGS = [
    (50220, 8080),
    (50221, 8081),
    (50222, 8082),
]


def _has(name: str, host: int, sb_port: int) -> bool:
    return any(
        m.get("host_port") == host and m.get("sandbox_port") == sb_port
        for m in sbx.ports(name)
    )


def _wait_until(predicate, *, timeout: float = 5.0) -> bool:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return True
        time.sleep(0.1)
    return False


@pytest.mark.timeout(120)
def test_multi_port_publish_independence(sandbox_name):
    kit = materialize_kit()
    anchor = bring_up_anchored(sandbox_name, kit)
    specs = [f"{h}:{s}" for h, s in MAPPINGS]
    try:
        # Publish all three.
        for spec in specs:
            r = subprocess.run(
                ["sbx", "ports", sandbox_name, "--publish", spec],
                capture_output=True, timeout=15,
            )
            assert r.returncode == 0, (
                f"publish {spec} failed: stdout={r.stdout!r} stderr={r.stderr!r}"
            )

        # Each appears (allow brief list-visibility lag per Quirk #3).
        for host, sb_port in MAPPINGS:
            assert _wait_until(lambda h=host, s=sb_port: _has(sandbox_name, h, s)), (
                f"mapping {host}:{sb_port} never appeared after publish"
            )

        # Independence test: unpublish the middle one. The other two
        # must stay.
        mid_host, mid_sb = MAPPINGS[1]
        r = subprocess.run(
            ["sbx", "ports", sandbox_name, "--unpublish", f"{mid_host}:{mid_sb}"],
            capture_output=True, timeout=15,
        )
        assert r.returncode == 0, (
            f"unpublish failed: stdout={r.stdout!r} stderr={r.stderr!r}"
        )
        assert _wait_until(lambda: not _has(sandbox_name, mid_host, mid_sb)), (
            "middle mapping never disappeared after unpublish"
        )

        # The other two are still there.
        for host, sb_port in (MAPPINGS[0], MAPPINGS[2]):
            assert _has(sandbox_name, host, sb_port), (
                f"unpublishing {mid_host}:{mid_sb} also affected "
                f"{host}:{sb_port} — mappings are NOT independent"
            )
    finally:
        for spec in specs:
            subprocess.run(
                ["sbx", "ports", sandbox_name, "--unpublish", spec],
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
