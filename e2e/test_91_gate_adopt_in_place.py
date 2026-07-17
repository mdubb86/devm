"""91: `devm shell` adopts a running-but-unprovisioned VM in place.

The boot-integrity gate (Task 1) makes the base image boot locked and
inert: `devm.target` is installed but disabled, so a VM the daemon
didn't drive through provisioning — a direct `tart run` outside devm,
most notably — comes up with no ssh/caddy/dnsmasq/egress. Task 7 taught
`devm shell` to recognize that shape (VM running, `devm.target`
inactive, no dirty-provisioning marker) and adopt it in place: run the
same provisioning tail as a cold start directly against the already-
running VM, WITHOUT `StartVM`/`tart delete` — same disk, same VM name,
no teardown.

This test drives the whole path for real:
  1. `devm shell` cold-starts a project normally (VM gets provisioned,
     disk has real state).
  2. `devm stop` powers the guest off cleanly (disk preserved).
  3. The SAME VM is booted raw via `tart run`, bypassing the daemon
     entirely — this reproduces exactly the locked/inert shape the
     gate produces for a non-devm boot.
  4. `devm shell` again: the daemon must recognize the running-but-
     unprovisioned VM and adopt it — no `StartVM`, no teardown, same
     disk (a sentinel file planted before the raw boot must survive).

What it doesn't cover (tested elsewhere):
  - The base image's locked/inert floor itself -> test_90.
  - Warm-attach (already provisioned) / cold-start (stopped) paths ->
    test_01, test_50.
  - Teardown-dirty recovery (interrupted provisioning) -> unit-tested
    in internal/orchestrator/shell_test.go
    (TestRunShellRunning_TargetInactiveMarkerPresent_TeardownAndColdStart).
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm

SENTINEL_FILE = "/home/devm/.adopt-in-place-sentinel"
SENTINEL_CONTENT = "planted-before-raw-boot"


def _wait_exec_ready(vm: TartSandbox, timeout: float = 90.0) -> bool:
    """Poll `tart exec <vm> true` until the guest agent answers.

    Mirrors conftest.py's base_clone fixture readiness poll: a hung
    single attempt (agent not listening yet) must not abort the whole
    wait, so each attempt gets its own bounded timeout.
    """
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        try:
            r = subprocess.run(
                ["tart", "exec", vm.name, "true"],
                capture_output=True, timeout=5,
            )
            if r.returncode == 0:
                return True
        except subprocess.TimeoutExpired:
            pass
        time.sleep(1)
    return False


@pytest.mark.slow
@pytest.mark.timeout(420)
def test_adopt_in_place(devm, workspace, sandbox_name):
    workspace.write_devmyaml()
    vm = TartSandbox(name=sandbox_name)

    # ---- 1. Normal cold-start: real provisioning, real disk state. ----
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"
    assert vm.state() == "running", f"expected running after cold-start, got {vm.state()!r}"

    # Plant a sentinel on the guest disk BEFORE the raw boot. Its
    # survival through the raw-boot + adopt cycle is the concrete proof
    # that adopt-in-place never wiped/recreated the disk (no `tart
    # delete` happened) -- unlike a teardown+cold-start, which would
    # start from a fresh devm-base clone.
    plant = vm.exec("bash", "-c", f"echo {SENTINEL_CONTENT} > {SENTINEL_FILE}")
    assert plant.ok, f"failed to plant sentinel: {plant.stderr!r}"

    # ---- 2. `devm stop` -- clean poweroff, disk preserved. ----
    devm.stop(yes=True)
    stopped = vm.wait_state("stopped", timeout=30.0)
    assert stopped == "stopped", f"expected VM stopped after `devm stop`, got {stopped!r}"

    # ---- 3. Boot the SAME VM raw via `tart run`, bypassing the daemon. ----
    proc = subprocess.Popen(
        ["tart", "run", "--no-graphics", sandbox_name],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    try:
        assert vm.wait_running(timeout=60.0), "raw `tart run` never reached running"
        assert _wait_exec_ready(vm), "raw-booted VM never became reachable via tart exec"

        # Precondition the rest of the test depends on: this really is
        # the gate's locked/inert shape, not an already-provisioned VM.
        target_state = vm.exec("systemctl", "is-active", "devm.target").stdout.strip()
        assert target_state != "active", (
            f"expected devm.target inactive on a daemon-less boot; got {target_state!r}"
        )

        # ---- 4. `devm shell` again: must adopt in place. ----
        r2 = subprocess.run(
            [devm.path, "shell", "--", "echo", "adopted-and-running"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r2.returncode == 0, (
            f"devm shell should adopt the running-but-unprovisioned VM and "
            f"exit 0; got rc={r2.returncode}\nstderr={r2.stderr.decode()}"
        )
        assert b"adopted-and-running" in r2.stdout, (
            f"command should have run inside the adopted VM; stdout={r2.stdout!r}"
        )

        stderr2 = r2.stderr.decode()
        assert "adopting running vm" in stderr2, (
            f"expected the adopt-in-place branch's status line in stderr; got:\n{stderr2}"
        )
        # Precise branch check: the cold-start-only steps must NOT have
        # run -- adopt-in-place skips StartVM and the exec-ready poll.
        assert "starting vm" not in stderr2, (
            f"adopt-in-place must not go through the cold-start StartVM step; stderr:\n{stderr2}"
        )
        assert "waiting for vm ready" not in stderr2, (
            f"adopt-in-place must not go through the cold-start ready-poll step; stderr:\n{stderr2}"
        )

        # ---- Assertions: enforced, same VM, same disk. ----
        # devm.target is now active -- the composed script ran its full
        # tail (enforce -> services -> systemctl start devm.target)
        # directly against the already-running VM.
        target_after = vm.exec("systemctl", "is-active", "devm.target").stdout.strip()
        assert target_after == "active", (
            f"expected devm.target active after adopt-in-place provisioning; got {target_after!r}"
        )

        # Same VM name (trivially true -- we never changed it) and same
        # disk: the sentinel planted before the raw boot survived the
        # whole cycle, proving no teardown/recreate occurred.
        assert vm.state() == "running"
        sentinel = vm.exec("cat", SENTINEL_FILE)
        assert sentinel.ok and sentinel.stdout.strip() == SENTINEL_CONTENT, (
            f"sentinel planted before the raw boot did not survive adopt-in-place "
            f"(disk was wiped/recreated instead of adopted): "
            f"ok={sentinel.ok} stdout={sentinel.stdout!r} stderr={sentinel.stderr!r}"
        )
    finally:
        # Power the guest off cleanly through devm (raw `tart stop`
        # crashes the guest -- see internal note on tart's stop
        # semantics), then reap our directly-spawned `tart run` process.
        subprocess.run(
            [devm.path, "stop", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        try:
            proc.wait(timeout=30)
        except subprocess.TimeoutExpired:
            proc.terminate()
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait(timeout=10)
