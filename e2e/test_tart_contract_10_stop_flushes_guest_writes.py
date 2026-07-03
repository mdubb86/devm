"""Pin: `tart stop <name>` gives the guest OS enough time to complete a
graceful shutdown, so a file the guest sync'd before stop survives
across a subsequent `tart run`.

Bug J's fix routes `/vm/stop` through `tart stop <name>` before the
supervisor SIGTERMs the tart-run process — precisely to give the guest
this window. If a future tart version regressed to a SIGKILL-style
shutdown, tests like devm's test_52 would flake in the field but the
tart-level cause would be invisible. This contract surfaces the failure
at its true layer.

The guest issues an explicit `sync` before we ask tart to stop; the
contract is that tart's stop doesn't tear the process down before that
sync's write hits the image. (Testing an unsync'd write's survival is
too tight — the kernel writeback timing is nondeterministic and not
what devm actually depends on.)

What this pins:
  - After `tart stop`, the VM transitions to `stopped`.
  - A sync'd write inside the guest is present on the next `tart run`.
"""
from __future__ import annotations

import secrets
import subprocess
import time

import pytest

from helpers import registry
from helpers.tart import TartSandbox


TEMPLATE = "ghcr.io/cirruslabs/debian:latest"


@pytest.fixture
def owned_vm():
    """Boot a fresh cirruslabs debian VM; delete on exit.

    Separate from inspector_vm (session-scoped) because this test needs
    a stop/restart cycle that would fight with the shared inspector.
    """
    name = f"contract-stop-{secrets.token_hex(2)}"
    registry.append("sandbox", name)
    proc = None
    try:
        subprocess.run(["tart", "pull", TEMPLATE], check=True, timeout=300)
        subprocess.run(["tart", "clone", TEMPLATE, name], check=True, timeout=60)

        def run_bg():
            return subprocess.Popen(
                ["tart", "run", "--no-graphics", name],
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            )

        proc = run_bg()
        vm = TartSandbox(name=name)
        assert vm.wait_running(timeout=120), f"{name} never reached running"
        for _ in range(60):
            if vm.exec("true").ok:
                break
            time.sleep(1)
        else:
            raise RuntimeError(f"{name} never became exec-ready")
        yield vm, run_bg, proc
    finally:
        subprocess.run(["tart", "stop", name], capture_output=True, timeout=30)
        subprocess.run(["tart", "delete", name], capture_output=True, timeout=15)
        registry.remove("sandbox", name)


@pytest.mark.contract
def test_tart_stop_persists_synced_writes(owned_vm):
    """Write a file, sync, `tart stop`, `tart run`, file must be present.

    The `sync` is deliberate: we're pinning that tart's stop path
    doesn't tear the guest down BEFORE its own sync commits. Testing an
    unsync'd write is too tight — that would gate on kernel writeback
    timing, which is a Linux concern, not tart.
    """
    vm, run_bg, first_proc = owned_vm

    r = vm.exec_shell("echo persist > /home/admin/tart-stop-probe && sync")
    assert r.ok, f"write failed: {r.stderr!r}"

    r = subprocess.run(["tart", "stop", vm.name], capture_output=True, timeout=30)
    assert r.returncode == 0, f"tart stop failed: {r.stderr.decode()!r}"

    first_proc.wait(timeout=30)

    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        if vm.state() == "stopped":
            break
        time.sleep(0.5)
    assert vm.state() == "stopped", (
        f"expected stopped after tart stop; got {vm.state()!r}"
    )

    proc = run_bg()
    try:
        assert vm.wait_running(timeout=120), "VM did not come back up"
        for _ in range(60):
            if vm.exec("true").ok:
                break
            time.sleep(1)
        else:
            raise RuntimeError("VM did not become exec-ready after restart")

        r = vm.exec("cat", "/home/admin/tart-stop-probe")
        assert r.ok, (
            f"marker not present after tart stop + tart run — tart's shutdown "
            f"did not flush the guest's writeback cache: exit={r.exit_code} "
            f"stderr={r.stderr!r}"
        )
        assert r.stdout.strip() == "persist", (
            f"marker content wrong after restart: {r.stdout!r}"
        )
    finally:
        subprocess.run(["tart", "stop", vm.name], capture_output=True, timeout=30)
        try:
            proc.wait(timeout=30)
        except subprocess.TimeoutExpired:
            proc.kill()
