"""88: guest system clock heals from skew via the daemon's SNTP responder.

Simulates a post-Mac-sleep scenario: the guest's system clock is deep in
the past. systemd-timesyncd is configured to sync from MAC_HOST (via
nftables DNAT to the daemon's SNTP responder). Within
PollIntervalMaxSec (64s) timesyncd polls, our responder answers from the
host wall clock, and `date` heals.

What this pins:
  - timesyncd runs in the guest and points at MAC_HOST (drop-in config
    applied by the provisioner).
  - nftables DNAT for UDP:123 routes to MAC_HOST:<daemon_ntp_port>.
  - Setting the guest clock backwards is detected and healed by the
    next poll (bounded by PollIntervalMaxSec + a safety margin).

What it doesn't cover:
  - Real Mac sleep. That would require darwin-level power-management
    hooks; skew-injection via `date -s` reproduces the same guest-side
    state timesyncd sees post-wake.
  - The NTP wire format itself. Covered by
    internal/serviceapi/ntp_test.go's TestNTPServer_LivePortEndToEnd.
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm


# 64s max poll interval + generous headroom. The first poll after
# `systemctl restart systemd-timesyncd` (which the provisioner does)
# should fire within ~1-2s, so realistic heal is well under 30s. Give
# the test 90s so a slow CI machine doesn't flake.
HEAL_TIMEOUT = 90
HEAL_POLL = 3


@pytest.mark.slow
@pytest.mark.timeout(360)
def test_clock_heals_after_forced_skew(workspace, devm):
    workspace.write_devmyaml(install=["true"])

    start = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=240,
    )
    assert start.returncode == 0, (
        f"devm start failed: rc={start.returncode}\n"
        f"stderr={start.stderr.decode()!r}"
    )

    # Sanity: guest clock is close to host at start.
    def guest_epoch() -> int:
        r = devm_exec_with_retry(
            devm.path,
            ["date", "-u", "+%s"],
            cwd=str(workspace.path),
            timeout=15,
        )
        assert r.returncode == 0, (
            f"date query failed: rc={r.returncode} stderr={r.stderr.decode()!r}"
        )
        return int(r.stdout.decode().strip().splitlines()[-1])

    baseline = guest_epoch()
    host_now = int(time.time())
    assert abs(baseline - host_now) < 30, (
        f"guest clock should start near host now={host_now}, got baseline={baseline}"
    )

    # Force a backwards skew big enough that TLS certs read as
    # "not yet valid" (the post-Mac-sleep symptom this feature heals),
    # but well inside the kernel's accepted range for settimeofday.
    # Two moves matter:
    #
    #  1. Stop timesyncd before setting. With timesyncd actively
    #     holding the clock via ntp_adjtime, `settimeofday` from
    #     another writer races (and can return EINVAL). A real Mac
    #     sleep has the same shape — timesyncd was paused during
    #     host sleep too.
    #
    #  2. Skew by 24 hours, not 56 years. Some hardened kernel
    #     configs reject settimeofday values before the system's
    #     first-boot timestamp; 24h back is unambiguously safe and
    #     more than enough to trip TLS not-yet-valid.
    stop_ts = devm_exec_with_retry(
        devm.path,
        ["sudo", "systemctl", "stop", "systemd-timesyncd"],
        cwd=str(workspace.path),
        timeout=15,
    )
    assert stop_ts.returncode == 0, (
        f"stop timesyncd: rc={stop_ts.returncode}\nstderr={stop_ts.stderr.decode()!r}"
    )
    skew_target = baseline - 86400  # 24h backward
    skew = devm_exec_with_retry(
        devm.path,
        ["sudo", "date", "-s", f"@{skew_target}"],
        cwd=str(workspace.path),
        timeout=15,
    )
    assert skew.returncode == 0, (
        f"date -s failed: rc={skew.returncode}\nstderr={skew.stderr.decode()!r}"
    )

    # Confirm skew took effect. Allow generous slack — we don't care
    # about drift precision, only that the clock is well behind host
    # by at least many hours so heal is observable.
    after_skew = guest_epoch()
    host_now = int(time.time())
    assert host_now - after_skew > 3600, (
        f"guest clock should be at least an hour behind host after skew; "
        f"guest={after_skew} host={host_now}"
    )

    # Poke timesyncd — restart it to force an immediate poll instead of
    # waiting up to PollIntervalMaxSec. This mirrors what a well-behaved
    # boot / resume hook would do; the primary pin is that timesyncd
    # CAN reach the daemon's responder at all. If bounded poll wait
    # ends up mattering for real users, that's a separate test.
    poke = devm_exec_with_retry(
        devm.path,
        ["sudo", "systemctl", "restart", "systemd-timesyncd"],
        cwd=str(workspace.path),
        timeout=15,
    )
    assert poke.returncode == 0, (
        f"restart timesyncd: rc={poke.returncode}\nstderr={poke.stderr.decode()!r}"
    )

    # Wait for heal.
    deadline = time.time() + HEAL_TIMEOUT
    healed = False
    latest = after_skew
    while time.time() < deadline:
        latest = guest_epoch()
        host_now = int(time.time())
        if abs(latest - host_now) < 60:
            healed = True
            break
        time.sleep(HEAL_POLL)
    assert healed, (
        f"guest clock never healed within {HEAL_TIMEOUT}s; last guest epoch={latest}, "
        f"host now={int(time.time())}"
    )
