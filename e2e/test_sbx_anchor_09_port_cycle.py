"""sbx-anchor 09: publish → verify → unpublish → verify → re-publish
cycle for a single port mapping, all under a live anchor.

Pins the basic invariant the new architecture relies on: ports are
sandbox-scoped, not session-scoped, when the session never ends.
Each step's effect should be immediately visible in `sbx ports
--json` (with a small tolerance for the publish→list lag sbx
exhibits).
"""
from __future__ import annotations
import subprocess
import time

import pytest

from helpers import sbx
from helpers.sbx_kit import bring_up_anchored, materialize_kit

pytestmark = pytest.mark.sbx


SANDBOX_PORT = 8090
HOST_PORT = 50210


def _has_mapping(name: str, host: int, sandbox_port: int) -> bool:
    return any(
        m.get("host_port") == host and m.get("sandbox_port") == sandbox_port
        for m in sbx.ports(name)
    )


def _wait_mapping(name: str, host: int, sandbox_port: int, *, present: bool, timeout: float = 5.0) -> None:
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if _has_mapping(name, host, sandbox_port) == present:
            return
        time.sleep(0.1)
    raise AssertionError(
        f"sbx ports {name} never reached present={present} for "
        f"{host}:{sandbox_port} within {timeout}s"
    )


@pytest.mark.timeout(120)
def test_publish_unpublish_cycle_under_anchor(sandbox_name):
    kit = materialize_kit()
    anchor = bring_up_anchored(sandbox_name, kit)
    spec = f"{HOST_PORT}:{SANDBOX_PORT}"
    try:
        # 1. Publish.
        r = subprocess.run(
            ["sbx", "ports", sandbox_name, "--publish", spec],
            capture_output=True, timeout=15,
        )
        assert r.returncode == 0, (
            f"publish failed: stdout={r.stdout!r} stderr={r.stderr!r}"
        )
        _wait_mapping(sandbox_name, HOST_PORT, SANDBOX_PORT, present=True)

        # 2. Unpublish.
        r = subprocess.run(
            ["sbx", "ports", sandbox_name, "--unpublish", spec],
            capture_output=True, timeout=15,
        )
        assert r.returncode == 0, (
            f"unpublish failed: stdout={r.stdout!r} stderr={r.stderr!r}"
        )
        _wait_mapping(sandbox_name, HOST_PORT, SANDBOX_PORT, present=False)

        # 3. Re-publish same spec — sandbox-scoped ports allow this.
        r = subprocess.run(
            ["sbx", "ports", sandbox_name, "--publish", spec],
            capture_output=True, timeout=15,
        )
        assert r.returncode == 0, (
            f"re-publish failed: stdout={r.stdout!r} stderr={r.stderr!r}"
        )
        _wait_mapping(sandbox_name, HOST_PORT, SANDBOX_PORT, present=True)
    finally:
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
