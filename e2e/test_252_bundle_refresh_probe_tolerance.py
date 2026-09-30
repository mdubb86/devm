"""252: `gdevm upgrade` restarts gdevm-serve.service in the guest to
swap in a fresh binary (see internal/serviceapi/refresh_bundle.go).
That's a brief window where the reserved _devm.<project>.test health
route the Mac watchdog probes is unreachable. The watchdog's
proxy-listener check tolerates a single failed probe with one retry
before declaring drift (internal/serviceapi/watchdog_check_proxy_listener.go's
proxyListenerRetryDelay) — this test proves that tolerance holds
end-to-end: triggering a real bundle refresh must not flip `devm
status --json`'s health.proxy_healthy false for any observer polling
across the refresh window, and the listener must still be healthy
(not wedged) once things settle past a full watchdog tick.

Spec cross-reference: docs/superpowers/specs/2026-09-29-gdevm-serve-guest-daemon.md
Review focus #1 ("Bundle refresh race").
"""
from __future__ import annotations

import json
import subprocess
import threading
import time

import pytest

pytestmark = pytest.mark.devm

# The watchdog's tick is 60s (internal/serviceapi/runner.go:
# `NewStateWatchdog(cache, gt, checks, 60*time.Second)`). Polling for
# longer than one full tick after triggering the refresh guarantees at
# least one real watchdog probe lands somewhere in (or shortly after)
# the refresh window, rather than relying on luck.
_POLL_SECONDS = 75
_POLL_INTERVAL_SECONDS = 0.5


def _status_json(devm_path: str, cwd: str) -> dict | None:
    """Returns the parsed status body, or None if the call itself
    failed (recorded as a sample rather than raising, so a transient
    CLI-to-daemon hiccup during the refresh shows up in the failure
    message instead of aborting the poll loop early)."""
    r = subprocess.run(
        [devm_path, "status", "--json"],
        cwd=cwd, capture_output=True, timeout=10,
    )
    if r.returncode != 0:
        return None
    try:
        return json.loads(r.stdout.decode())
    except json.JSONDecodeError:
        return None


@pytest.mark.slow
@pytest.mark.timeout(420)
def test_bundle_refresh_does_not_flip_proxy_unhealthy(devm, workspace):
    workspace.write_devmyaml(no_repo=True)
    try:
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed: {r.stderr.decode()!r}"

        j = _status_json(devm.path, str(workspace.path))
        assert j is not None and j["health"]["proxy_healthy"] is True, (
            f"precondition: proxy must be healthy before triggering a refresh: {j}"
        )

        # Poll status on a tight interval, starting just before the
        # refresh and continuing well past one watchdog tick, while the
        # refresh itself runs on a separate thread. Every observed
        # sample's health.proxy_healthy must be true — a single false
        # reading anywhere in the window is the false-respawn-trigger
        # regression this test exists to catch.
        observations: list[bool] = []
        failed_calls = 0
        stop = threading.Event()

        def poll() -> None:
            nonlocal failed_calls
            deadline = time.monotonic() + _POLL_SECONDS
            while time.monotonic() < deadline and not stop.is_set():
                body = _status_json(devm.path, str(workspace.path))
                if body is None:
                    failed_calls += 1
                else:
                    observations.append(body["health"]["proxy_healthy"])
                time.sleep(_POLL_INTERVAL_SECONDS)

        poller = threading.Thread(target=poll, daemon=True)
        poller.start()

        upgrade = subprocess.run(
            [devm.path, "exec", "gdevm", "upgrade"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert upgrade.returncode == 0, (
            f"gdevm upgrade failed: stdout={upgrade.stdout.decode()!r} "
            f"stderr={upgrade.stderr.decode()!r}"
        )

        poller.join(timeout=_POLL_SECONDS + 15)
        stop.set()

        assert observations, "poller never got a successful `devm status --json` sample"
        assert all(observations), (
            f"proxy_healthy flipped false during/after the bundle refresh "
            f"({observations.count(False)} of {len(observations)} samples unhealthy); "
            f"{failed_calls} status calls failed outright. The watchdog's "
            "proxy-listener retry tolerance did not cover the refresh window."
        )

        # Settle check: gdevm-serve.service is active again in the
        # guest (the refresh's restart actually completed cleanly, not
        # left the unit down), and the reserved health route is
        # reachable — proves no lasting wedge, independent of what the
        # poller happened to sample.
        active = subprocess.run(
            [devm.path, "exec", "systemctl", "is-active", "gdevm-serve.service"],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert active.returncode == 0 and active.stdout.decode().strip() == "active", (
            f"gdevm-serve.service not active after refresh: "
            f"stdout={active.stdout.decode()!r} stderr={active.stderr.decode()!r}"
        )

        # Mac-side probe: devm-e2e's DNS resolves _devm.<project>.e2e.test
        # to the project's Mac loopback alias; the daemon's proxy dispatches
        # to softnet-exposed :8940, which forwards into the guest to
        # gdevm serve. Guest-side DNS doesn't know this hostname.
        hostname = f"_devm.{workspace.vm_name}.e2e.test"
        probe = subprocess.run(
            ["curl", "-sf", "-m", "5", f"http://{hostname}/v1/health"],
            capture_output=True, timeout=15,
        )
        assert probe.returncode == 0, (
            f"reserved health route unreachable after refresh settled: "
            f"stdout={probe.stdout.decode()!r} stderr={probe.stderr.decode()!r}"
        )

        j = _status_json(devm.path, str(workspace.path))
        assert j is not None and j["health"]["proxy_healthy"] is True, (
            f"proxy_healthy not true after refresh settled: {j}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
