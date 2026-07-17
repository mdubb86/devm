"""52: devm stop preserves disk state across stop+restart; /tmp is wiped.

Consolidated (was also test_54, test_71): test_54 was a near-exact
duplicate of this scenario (cold-start -> plant a marker -> stop ->
restart -> assert marker survived, i.e. restart reused disk instead of
recreating); test_71 was the same skeleton with the opposite
assertion, since /tmp is tmpfs and gets wiped by the real boot cycle.
One boot now plants both a disk marker and a /tmp probe before the
same stop+restart cycle and checks both outcomes.

What this pins:
  - devm stop --yes transitions running -> stopped.
  - Disk state (marker file under /home/devm) survives the stop.
  - Subsequent `devm shell` restarts the existing VM (reuses disk, not
    recreate) — proven by the marker's survival.
  - A file written to /tmp before stop is ABSENT after restart (tmpfs
    wiped by the real boot cycle, not a resume).

What it doesn't cover (tested elsewhere):
  - Teardown (removal) -> test_53.
  - Interactive stop prompt -> test_03.
  - Volume/mount data persisting across stop/restart -> test_70.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_stop_preserves_disk_wipes_tmp(devm, workspace, tart_sandbox):
    # tart_sandbox fixture already cold-started the VM.
    assert tart_sandbox.state() == "running"

    # Plant a disk marker (was test_52/test_54) and a /tmp probe (was
    # test_71) before stop. sync() forces the marker write to disk so
    # page-cache races don't mask the restart-vs-recreate signal.
    r = tart_sandbox.exec_shell(
        "echo hello > /home/devm/marker.txt && sync && echo probe > /tmp/probe-tmp"
    )
    assert r.exit_code == 0, f"failed to plant marker/probe: {r.stderr}"

    # Sanity: probe present immediately.
    r = tart_sandbox.exec("test", "-f", "/tmp/probe-tmp")
    assert r.exit_code == 0, "/tmp/probe-tmp should exist immediately after write"

    # Stop the VM via --yes. (This also subsumes test_03's "yes" param
    # — the running->stopped transition under --yes is asserted right
    # here; test_03 keeps only the "prompt" arm, which is otherwise
    # untested.)
    devm.stop(yes=True, timeout=30)

    stopped_state = tart_sandbox.wait_state("stopped", timeout=15)
    assert stopped_state == "stopped", (
        f"VM should be 'stopped' after devm stop; got {stopped_state!r}"
    )

    # Restart via devm shell -- true (real boot cycle, not a resume).
    subprocess.run(
        [devm.path, "shell", "--", "true"],
        capture_output=True, cwd=str(workspace.path), timeout=180,
    )

    running_state = tart_sandbox.wait_state("running", timeout=30)
    assert running_state == "running", (
        f"VM should be 'running' after restart; got {running_state!r}"
    )

    # Disk marker survived -> restart reused disk, didn't recreate.
    check = tart_sandbox.exec_shell("cat /home/devm/marker.txt")
    assert check.exit_code == 0, f"marker missing after restart: {check.stderr}"
    assert check.stdout.strip() == "hello", (
        f"marker corrupted after restart: {check.stdout!r}"
    )

    # /tmp probe must be ABSENT -> Tart's boot cycle wipes /tmp.
    r = tart_sandbox.exec("test", "-f", "/tmp/probe-tmp")
    assert r.exit_code != 0, (
        "/tmp/probe-tmp is still present after stop+restart — "
        "Tart is no longer providing a clean /tmp on boot. "
        "If this is expected, update the supervision design to re-add "
        "the explicit `rm -rf /tmp/.devm` cleanup step at startup."
    )
