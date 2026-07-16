"""89: `startup:` runs on EVERY boot, with open egress, and iron-proxy
enforcement is still applied afterward.

Contrast with `install:` (test_88): `install:` is first-boot-only and
gated by /var/lib/devm/provisioned. `startup:` has no such gate — it's
meant for state that needs re-establishing on every boot (e.g. mounts,
tunnels) and runs BEFORE egress enforcement locks the guest down, same
as `install:` does (test_83). This test pins three things in one
project:

  1. Every boot: `startup:` appends to a counter file. Cold-start
     (`devm shell`) leaves the counter at 1; a `devm stop` + `devm
     shell` restart bumps it to 2 — unlike `install:`, which stays 1
     across a restart.
  2. Open egress during `startup:`: one of the startup commands curls
     a host (example.com) deliberately NOT on `network.allow` (which
     allows only api.github.com — an empty allow list means allow-all
     at the iron-proxy layer, so a real host must be listed for the
     block below to bite). It must succeed, proving `startup:` ran
     before enforcement was applied.
  3. Enforcement intact after boot: a declared service (`exec:`, which
     starts only After=devm-enforce.service — see render/systemd.go's
     RenderService afterEnforce path) curls the SAME non-allow-listed
     host. That curl must FAIL — proving devm-enforce.service (which
     masking nftables.service hands boot-restore off to, per
     provision.go's setupBootEnforcement) actually re-applies the
     egress policy and doesn't leave the VM open. Cross-checked with a
     direct `devm shell` probe of the same host post-boot, mirroring
     test_83's post-install enforcement check.

Devm dependency: /etc/systemd/system/devm-startup.service runs every
startup: command in order, Before=devm-enforce.service, on every boot
when startup: is non-empty (provision.go's setupBootEnforcement masks
the stock nftables.service and enables devm-enforce.service +
devm-startup.service instead). If that ordering or masking regresses,
this test breaks loudly.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers import stop_and_wait_stopped
from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm

# Absolute /home/devm paths (not $HOME) — devm-startup.service runs
# unqualified commands as root (no User= in RenderStartupUnit), so
# $HOME there would resolve to /root, not the guest user's home. Using
# an explicit path sidesteps that ambiguity, matching test_88's
# SENTINEL convention.
COUNT_FILE = "/home/devm/.startup-count"
STARTUP_FETCH_FILE = "/home/devm/.startup-fetch"
SVC_FETCH_FILE = "/home/devm/.svc-fetch"

# A real public host deliberately NOT on network.allow (which allows
# only api.github.com below). iron-proxy blocks non-allow-listed hosts
# only when the allow list is non-empty (empty = allow-all), matching
# test_43/test_83's pattern of allow-listing one host and curling a
# different one.
NON_ALLOWLISTED_HOST = "https://example.com"


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_startup_runs_every_boot_open_egress_enforced_after(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        startup=[
            f"echo run >> {COUNT_FILE}",
            f"curl -sf -m 10 {NON_ALLOWLISTED_HOST} -o {STARTUP_FETCH_FILE} || true",
        ],
        network={"allow": ["api.github.com"]},
        services={
            "probe": {
                "exec": [
                    "sh", "-c",
                    f"curl -sf -m 10 {NON_ALLOWLISTED_HOST} -o {SVC_FETCH_FILE} || true; "
                    "sleep infinity",
                ],
                "restart": "always",
            },
        },
    )

    def count() -> int:
        r = devm_exec_with_retry(
            devm.path, ["sh", "-c", f"wc -l < {COUNT_FILE} 2>/dev/null || echo 0"],
            cwd=str(workspace.path), timeout=30,
        )
        return int(r.stdout.decode().strip() or "0")

    def file_size(path: str) -> int:
        r = devm_exec_with_retry(
            devm.path, ["sh", "-c", f"wc -c < {path} 2>/dev/null || echo 0"],
            cwd=str(workspace.path), timeout=30,
        )
        return int(r.stdout.decode().strip() or "0")

    # ---- Cold-start. ----
    shell = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=480,
    )
    assert shell.returncode == 0, f"cold start failed: {shell.stderr.decode()!r}"

    # ---- Assertion 1 (every boot, part 1): counter is 1 after the
    # ---- first boot. ----
    assert count() == 1, "startup: should have run exactly once on first boot"

    # ---- Assertion 2: open egress during startup: — the curl to a
    # ---- non-allow-listed host succeeded because startup: ran before
    # ---- enforcement was applied. ----
    assert file_size(STARTUP_FETCH_FILE) > 0, (
        "startup:'s curl to a non-allow-listed host produced no/empty "
        f"output at {STARTUP_FETCH_FILE} — startup: should run under "
        "open egress, before enforcement locks the guest down"
    )

    # ---- Assertion 3: enforcement intact after boot — the exec:
    # ---- service (which starts only After=devm-enforce.service)
    # ---- curling the SAME non-allow-listed host must have FAILED
    # ---- (blocked), proving masking nftables.service + enabling
    # ---- devm-enforce.service did not leave the VM unenforced. ----
    assert file_size(SVC_FETCH_FILE) == 0, (
        "the exec: service's curl to a non-allow-listed host produced "
        f"non-empty output at {SVC_FETCH_FILE} — it should have been "
        "blocked by iron-proxy egress enforcement, which runs before "
        "declared services start"
    )

    # Cross-check assertion 3 directly (mirrors test_83's post-install
    # enforcement probe): a fresh `devm shell` curl to the same host,
    # run well after boot (enforcement is unconditionally applied by
    # this point), must also fail.
    probe = subprocess.run(
        [devm.path, "shell", "--", "curl", "-sf", "-m", "10", NON_ALLOWLISTED_HOST],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert probe.returncode != 0, (
        "post-boot curl to a non-allow-listed host should be blocked by "
        "iron-proxy enforcement, but it succeeded\n"
        f"stdout: {probe.stdout.decode()}\nstderr: {probe.stderr.decode()}"
    )

    # ---- Every boot, part 2: devm stop + devm shell restart bumps the
    # ---- counter to 2 — startup: re-ran, unlike install: (test_88),
    # ---- which stays at 1 across a restart. ----
    stop_and_wait_stopped(devm, sandbox_name)
    reshell = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert reshell.returncode == 0, f"restart failed: {reshell.stderr.decode()!r}"

    assert count() == 2, "startup: must re-run on every boot, including a stop/restart cycle"
