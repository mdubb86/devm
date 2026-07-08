"""89: recipe rules in inet devm_filter/user_output survive VM reboot.

The scaffold + snapshot-on-enforcement design gives recipes an escape
hatch from the default-deny egress firewall: an install: command runs
`nft add rule inet devm_filter user_output ...` and the rule sticks
across both apply-egress-enforcement (which fires later in the same
provision run) and full VM reboot (which fires systemd's
nftables.service re-loading /etc/nftables.conf).

This test uses a benign, verifiable marker rule (an oif on a fake
interface name with a comment) — we're pinning the mechanism, not
that any particular interface exists on the base image.

What this pins:
  - The `user_output` chain exists after install: (scaffold step ran).
  - A rule added during install: is present after apply-egress-
    enforcement (live-apply preserves user_output).
  - The same rule is present after `devm stop` + `devm start`
    (systemd reloads /etc/nftables.conf → include restores it).

What it doesn't cover:
  - The docker-in-VM scenario end-to-end (Docker isn't on the base
    image). Once a docker recipe exists we can add test_90 that
    exercises the actual container-DNAT case Supabase needs.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm


# A marker any nftables version renders back the same way. `oifname
# "docker0"` is what the docker recipe will use, so exercising the same
# matcher shape here doubles as a smoke test for the docker path.
MARKER_RULE = 'oifname "e2e-marker-if" accept'


@pytest.mark.slow
@pytest.mark.timeout(360)
def test_user_output_survives_reboot(workspace, devm):
    workspace.write_devmyaml(
        install=[
            # Recipe uses vanilla `nft add rule`. The scaffold step
            # created the empty user_output chain before install:, so
            # this succeeds without any devm-specific helper.
            f'sudo nft add rule inet devm_filter user_output {MARKER_RULE}',
        ],
    )

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

    def user_output_rules() -> str:
        r = devm_exec_with_retry(
            devm.path,
            ["sudo", "nft", "list", "chain", "inet", "devm_filter", "user_output"],
            cwd=str(workspace.path),
            timeout=15,
        )
        assert r.returncode == 0, (
            f"nft list chain failed: rc={r.returncode}\n"
            f"stderr={r.stderr.decode()!r}"
        )
        return r.stdout.decode()

    # Phase 1: after the full cold-start (including apply-egress-
    # enforcement), the marker rule survives in the live ruleset.
    after_cold = user_output_rules()
    assert MARKER_RULE in after_cold, (
        f"marker rule missing from user_output after cold-start "
        f"(apply-egress-enforcement must have flushed it):\n{after_cold}"
    )

    # Phase 2: the marker is also snapshotted to the persistence path,
    # so systemd's nftables.service will re-load it on the next boot.
    snap = devm_exec_with_retry(
        devm.path,
        ["sudo", "cat", "/etc/nftables.d/user_output.conf"],
        cwd=str(workspace.path),
        timeout=15,
    )
    assert snap.returncode == 0, (
        f"cat user_output.conf failed: rc={snap.returncode}\n"
        f"stderr={snap.stderr.decode()!r}"
    )
    assert MARKER_RULE in snap.stdout.decode(), (
        "marker rule not snapshotted to /etc/nftables.d/user_output.conf; "
        "on VM reboot this rule would be lost. Snapshot content:\n"
        + snap.stdout.decode()
    )

    # Phase 3: stop + start. `devm stop` calls `tart stop` (graceful
    # guest shutdown), then `devm start` boots the same disk image.
    # systemd's nftables.service runs `nft -f /etc/nftables.conf`
    # early in boot — the include line pulls user_output.conf back in.
    stop = subprocess.run(
        [devm.path, "stop", "--yes"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=60,
    )
    assert stop.returncode == 0, (
        f"devm stop failed: rc={stop.returncode}\n"
        f"stderr={stop.stderr.decode()!r}"
    )
    restart = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=240,
    )
    assert restart.returncode == 0, (
        f"devm start (after stop) failed: rc={restart.returncode}\n"
        f"stderr={restart.stderr.decode()!r}"
    )

    after_reboot = user_output_rules()
    assert MARKER_RULE in after_reboot, (
        f"marker rule missing from user_output after VM reboot — "
        f"the include mechanism didn't restore it. Live chain:\n{after_reboot}"
    )
