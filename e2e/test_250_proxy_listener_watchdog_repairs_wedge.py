"""250: cold-start a project, confirm the reserved _devm.<project>.test
health route (Mac reverse-proxy -> guest gdevm-serve :8940) is
registered and reachable, and `devm status --json`'s health.proxy_healthy
stays true across multiple watchdog ticks.

Simulating a real listener wedge from outside the daemon process isn't
practical (see task-10-brief.md's ruling) — the daemon owns the
listener FDs, and injecting a fake failing probe is only reachable from
inside the watchdog's own test harness (already covered by the Task 7
unit tests with a fake GroundTruth). This test instead locks the half
of the invariant an e2e test CAN prove end-to-end: the healthy path
stays healthy under the daemon's own real watchdog cadence — the
reserved health route resolves, the probe reports OK, and nothing
about the passage of several ticks (60s each) flips it unhealthy on
its own.
"""
from __future__ import annotations

import json
import subprocess
import time

import pytest

pytestmark = pytest.mark.devm

# Long enough to span at least two real watchdog ticks (60s each per
# runner.go's `NewStateWatchdog(cache, gt, checks, 60*time.Second)`)
# after cold-start, so a listener that only stays healthy by luck on
# the first probe would still get caught flapping on a later one.
_POLL_WINDOW_SECONDS = 150
_POLL_INTERVAL_SECONDS = 25


def _status_json(devm_path: str, cwd: str) -> dict:
    r = subprocess.run(
        [devm_path, "status", "--json"],
        cwd=cwd, capture_output=True, timeout=15,
    )
    assert r.returncode == 0, f"devm status --json failed: {r.stderr.decode()!r}"
    return json.loads(r.stdout.decode())


@pytest.mark.slow
@pytest.mark.timeout(420)
def test_reserved_health_route_stays_reachable(devm, workspace):
    workspace.write_devmyaml(no_repo=True)
    try:
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed: {r.stderr.decode()!r}"

        # Reachable immediately after start. The route is VM-mode
        # (dial target 127.0.0.1:8940 inside the guest, per
        # serviceapi.reservedHealthRoute) — probed from the Mac side,
        # same side the Mac watchdog's own ProxyListenerHealth check
        # ultimately exercises: _devm.<project>.test resolves via
        # /etc/resolver/test -> devm's DNS -> the project's pool IP ->
        # the Mac reverse proxy -> softnet-exposed :8940 in the guest.
        hostname = f"_devm.{workspace.vm_name}.test"
        probe = subprocess.run(
            ["curl", "-sf", "-m", "5", f"http://{hostname}/v1/health"],
            capture_output=True, timeout=15,
        )
        assert probe.returncode == 0, (
            f"reserved health route unreachable right after cold-start: "
            f"stdout={probe.stdout.decode()!r} stderr={probe.stderr.decode()!r}"
        )
        assert b'"ok":true' in probe.stdout or b'"ok": true' in probe.stdout, (
            f"health route did not report ok:true: {probe.stdout.decode()!r}"
        )

        # devm status --json reports proxy_healthy: true immediately
        # (seeded optimistically at /vm/start, per ead60f6).
        j = _status_json(devm.path, str(workspace.path))
        assert j["health"]["proxy_healthy"] is True, j["health"]

        # Poll across the watchdog's tick cadence — every sample must
        # stay healthy. A single sleep-then-check would miss a
        # transient flap that self-heals between the one check and the
        # end of the sleep; sampling repeatedly closes that gap.
        deadline = time.monotonic() + _POLL_WINDOW_SECONDS
        samples = 0
        while time.monotonic() < deadline:
            j = _status_json(devm.path, str(workspace.path))
            assert j["health"]["proxy_healthy"] is True, (
                f"proxy_healthy flipped unhealthy during steady state: {j['health']}"
            )
            samples += 1
            time.sleep(_POLL_INTERVAL_SECONDS)
        assert samples >= 3, f"expected at least 3 samples across the poll window, got {samples}"

        # And the health route itself is still reachable after riding
        # out several ticks.
        probe = subprocess.run(
            ["curl", "-sf", "-m", "5", f"http://{hostname}/v1/health"],
            capture_output=True, timeout=15,
        )
        assert probe.returncode == 0, (
            f"reserved health route unreachable after riding out watchdog ticks: "
            f"stdout={probe.stdout.decode()!r} stderr={probe.stderr.decode()!r}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
