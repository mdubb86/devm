"""91: `devm shell` adopts a running-but-unprovisioned VM in place, and
tears down + cold-starts fresh when it finds a dirty (interrupted
provisioning) marker instead.

The boot-integrity gate (Task 1) makes the base image boot locked and
inert: `devm.target` is installed but disabled, so a VM the daemon
didn't drive through provisioning — a direct `tart run` outside devm,
most notably — comes up with no ssh/caddy/dnsmasq/egress. Task 7 taught
`devm shell` to recognize that shape (VM running, `devm.target`
inactive, no dirty-provisioning marker) and adopt it in place: run the
same provisioning tail as a cold start directly against the already-
running VM, WITHOUT `StartVM`/`tart delete` — same disk, same VM name,
no teardown.

`test_adopt_in_place` drives the whole path for real:
  1. `devm shell` cold-starts a project normally (VM gets provisioned,
     disk has real state), including a `direct: true` host-process
     service — exercises the VMIP re-discovery fix
     (apply_iron_proxy.go: `ApplyIronProxy` re-discovers the guest IP
     via `tart ip` on adopt, since a prior `devm stop` already cleared
     any stashed ironProxyState.VMIP).
  2. `devm stop` powers the guest off cleanly (disk preserved).
  3. The SAME VM is booted raw via `tart run`, bypassing the daemon
     entirely — this reproduces exactly the locked/inert shape the
     gate produces for a non-devm boot. Tart may hand out a new DHCP
     lease on this boot, so the guest's IP can genuinely change here.
  4. `devm shell` again: the daemon must recognize the running-but-
     unprovisioned VM and adopt it — no `StartVM`, no teardown, same
     disk (a sentinel file planted before the raw boot must survive) —
     and the direct service must route to the (possibly new) guest IP,
     not a stale pre-stop IP and not loopback.

`test_teardown_dirty_recovers_with_fresh_cold_start` drives the sibling
branch: a VM found with the interrupted-provisioning marker present
(`/run/devm/provisioning`) must be torn down and cold-started fresh —
never adopted onto a dirty slate. Was previously unit-only
(internal/orchestrator/shell_test.go
TestRunShellRunning_TargetInactiveMarkerPresent_TeardownAndColdStart).

What it doesn't cover (tested elsewhere):
  - The base image's locked/inert floor itself -> test_90.
  - Warm-attach (already provisioned) / cold-start (stopped) paths ->
    test_01, test_50, test_92 (warm-attach branch pin).
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers.direct import (
    BANNER,
    dig_a as _dig_a,
    dns_addr as _dns_addr,
    get_routes as _get_routes,
    tcp_read_banner as _tcp_read_banner,
)
from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm

SENTINEL_FILE = "/home/devm/.adopt-in-place-sentinel"
SENTINEL_CONTENT = "planted-before-raw-boot"

# Host-process `direct: true` service (test_112b's shape) added to this
# test's project so the adopt-in-place path exercises real direct-
# service routing, not just devm.target activation.
DIRECT_PORT = 59122


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
@pytest.mark.timeout(600)
def test_adopt_in_place(devm, workspace, sandbox_name):
    hostname = f"{sandbox_name}-direct.test"
    # No `docker: true` -- a bare host-process direct service (test_112b's
    # shape), so the only extra first-boot cost is a quick apt install.
    # `packages:`/`install:` are first-boot-only (gated on the guest's own
    # /var/lib/devm/provisioned marker, which survives the stop+raw-boot
    # cycle below), so the adopt-in-place provisioning tail in step 4
    # skips this and stays fast.
    workspace.write_devmyaml(
        packages=["netcat-openbsd"],
        network={"allow": ["deb.debian.org", "security.debian.org"]},
        services={
            "nc": {
                "port": DIRECT_PORT,
                "hostname": hostname,
                "direct": True,
                "exec": ["sh", "-c",
                         f"printf '%s' '{BANNER.decode()}' | "
                         f"nc -lk -p {DIRECT_PORT}"],
                "restart": "always",
            },
        },
    )
    vm = TartSandbox(name=sandbox_name)

    # ---- 1. Normal cold-start: real provisioning, real disk state. ----
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"
    assert vm.state() == "running", f"expected running after cold-start, got {vm.state()!r}"

    # Baseline: the direct service works right after cold-start, before
    # the stop/raw-boot/adopt cycle -- isolates any post-adopt failure
    # to the adopt path itself rather than a broken direct-service setup.
    vm_ip_before = vm.ip()
    assert vm_ip_before, "could not get VM IP via `tart ip` after cold-start"
    baseline = None
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        baseline = _tcp_read_banner(vm_ip_before, DIRECT_PORT, BANNER, timeout=3)
        if baseline == BANNER:
            break
        time.sleep(1)
    assert baseline == BANNER, (
        f"baseline: direct service not reachable at {vm_ip_before}:"
        f"{DIRECT_PORT} right after cold-start (got {baseline!r})"
    )

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

        # ---- Direct-service routing after adopt: the VMIP re-discovery
        # ---- fix (apply_iron_proxy.go re-discovers the guest IP via
        # ---- `tart ip` on adopt, since a prior `devm stop` already
        # ---- cleared any stashed ironProxyState.VMIP). The raw `tart
        # ---- run` boot may have handed out a NEW DHCP lease, so this
        # ---- must reflect the CURRENT IP, not the stale pre-stop one
        # ---- and not loopback. ----
        vm_ip_after = vm.ip()
        assert vm_ip_after, "could not get VM IP via `tart ip` after adopt-in-place"

        project_id = workspace.slug
        routes = _get_routes()
        assert project_id in routes, f"no /routes entry for {project_id!r}: {routes}"
        entry = next((e for e in routes[project_id] if e["hostname"] == hostname), None)
        assert entry is not None, (
            f"{hostname!r} missing from routes after adopt-in-place: {routes[project_id]}"
        )
        assert entry.get("direct") is True, (
            f"route for {hostname!r} not marked direct after adopt-in-place: {entry}"
        )

        # Mac-side DNS: soft warn-and-continue under the isolated e2e
        # lane's ephemeral $DEVM_DNS_ADDR (see test_110's module
        # docstring KNOWN GAP) -- must not abort before the reachability
        # check below, which is this assertion's main subject.
        dns_host, dns_port = _dns_addr()
        if dns_port == 0:
            print(
                "WARNING: DEVM_DNS_ADDR is ephemeral in the isolated "
                "e2e lane; skipping the Mac-side DNS sub-assertion only "
                "(see test_110's module docstring KNOWN GAP). "
                "Continuing with the TCP reachability check against the "
                "re-discovered VM IP directly."
            )
        else:
            answer = _dig_a(hostname, dns_host, dns_port)
            assert answer == vm_ip_after and answer != "127.0.0.1", (
                f"after adopt-in-place, DNS should answer the "
                f"re-discovered guest IP {vm_ip_after!r} for direct "
                f"hostname {hostname!r} -- not a stale pre-stop IP "
                f"({vm_ip_before!r}) and not loopback; got {answer!r}"
            )

        got = None
        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            got = _tcp_read_banner(vm_ip_after, DIRECT_PORT, BANNER, timeout=3)
            if got == BANNER:
                break
            time.sleep(1)
        assert got == BANNER, (
            f"Mac could not read the expected banner from the direct "
            f"service at {vm_ip_after}:{DIRECT_PORT} after adopt-in-place "
            f"(got {got!r}) -- the re-discovered guest IP is not "
            f"actually routing to the service"
        )
        # Never loopback: nothing on the Mac itself listens on this
        # port, so a stale/empty VMIP resolving to 127.0.0.1 would fail
        # here too, reinforcing the "not loopback" contract even when
        # the DNS sub-assertion above is soft-skipped.
        assert _tcp_read_banner("127.0.0.1", DIRECT_PORT, BANNER, timeout=2) is None, (
            "the direct service must not be reachable via the Mac's own "
            "loopback -- it must route to the guest VM IP"
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


@pytest.mark.slow
@pytest.mark.timeout(420)
def test_teardown_dirty_recovers_with_fresh_cold_start(devm, workspace, sandbox_name):
    """A VM found with `/run/devm/provisioning` present (a previous
    provisioning run was interrupted mid-flight) must NEVER be adopted
    onto a dirty slate -- `devm shell` tears it down and cold-starts
    fresh instead. Sibling of `test_adopt_in_place`'s pristine-VM case;
    same raw-boot setup, opposite outcome.
    """
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
    # ABSENCE after recovery is the concrete proof that the daemon
    # actually tore down and recreated the VM from a fresh devm-base
    # clone, rather than adopting the dirty disk in place (the mirror
    # image of test_adopt_in_place's survival assertion).
    plant = vm.exec("bash", "-c", f"echo {SENTINEL_CONTENT} > {SENTINEL_FILE}")
    assert plant.ok, f"failed to plant sentinel: {plant.stderr!r}"

    # ---- 2. `devm stop` -- clean poweroff, disk preserved. ----
    devm.stop(yes=True)
    stopped = vm.wait_state("stopped", timeout=30.0)
    assert stopped == "stopped", f"expected VM stopped after `devm stop`, got {stopped!r}"

    # ---- 3. Boot the SAME VM raw via `tart run`, bypassing the daemon --
    # ---- reproduces the gate's locked/inert shape, same as
    # ---- test_adopt_in_place. ----
    proc = subprocess.Popen(
        ["tart", "run", "--no-graphics", sandbox_name],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    try:
        assert vm.wait_running(timeout=60.0), "raw `tart run` never reached running"
        assert _wait_exec_ready(vm), "raw-booted VM never became reachable via tart exec"

        target_state = vm.exec("systemctl", "is-active", "devm.target").stdout.strip()
        assert target_state != "active", (
            f"expected devm.target inactive on a daemon-less boot; got {target_state!r}"
        )

        # Plant the interrupted-provisioning marker `devm shell` looks
        # for -- render's inProgressMarker
        # (internal/render/provision.go:20), written before the
        # composed script starts and removed when it finishes. Its
        # presence here simulates a previous provisioning run that got
        # killed mid-flight (daemon crash, host sleep, killed exec).
        # /run is tmpfs -- wiped on every reboot, so /run/devm itself
        # (created by the composed script's own first line) doesn't
        # exist yet on this fresh raw boot; recreate it first.
        mkdir = vm.exec("sudo", "mkdir", "-p", "/run/devm")
        assert mkdir.ok, f"failed to create /run/devm: {mkdir.stderr!r}"
        marker = vm.exec("sudo", "touch", "/run/devm/provisioning")
        assert marker.ok, f"failed to plant dirty marker: {marker.stderr!r}"
        marker_check = vm.exec("test", "-f", "/run/devm/provisioning")
        assert marker_check.ok, "dirty marker did not persist on the guest disk"

        # ---- 4. `devm shell` again: must tear down + cold-start fresh,
        # ---- NOT adopt. ----
        r2 = subprocess.run(
            [devm.path, "shell", "--", "echo", "recovered-fresh"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r2.returncode == 0, (
            f"devm shell should recover the dirty VM (teardown + cold-start) "
            f"and exit 0; got rc={r2.returncode}\nstderr={r2.stderr.decode()}"
        )
        assert b"recovered-fresh" in r2.stdout, (
            f"command should have run inside the freshly cold-started VM; "
            f"stdout={r2.stdout!r}"
        )

        stderr2 = r2.stderr.decode()
        assert "recovering (teardown + fresh start)" in stderr2, (
            f"expected the teardown-dirty branch's status line in stderr; got:\n{stderr2}"
        )
        assert "starting vm" in stderr2, (
            f"teardown-dirty recovery must fall through to the cold-start "
            f"StartVM step; stderr:\n{stderr2}"
        )
        # Precise branch check: this must NOT have taken the
        # adopt-in-place branch.
        assert "adopting running vm" not in stderr2, (
            f"a dirty (interrupted-provisioning) VM must never be adopted "
            f"in place; stderr:\n{stderr2}"
        )

        # ---- Assertions: recreated VM, enforced, sentinel gone. ----
        target_after = vm.exec("systemctl", "is-active", "devm.target").stdout.strip()
        assert target_after == "active", (
            f"expected devm.target active after teardown-dirty recovery; got {target_after!r}"
        )
        assert vm.state() == "running"

        sentinel = vm.exec("cat", SENTINEL_FILE)
        assert not sentinel.ok, (
            f"sentinel planted before the dirty-marker boot SURVIVED recovery "
            f"-- the VM was adopted in place instead of torn down and "
            f"recreated from a fresh disk: ok={sentinel.ok} "
            f"stdout={sentinel.stdout!r} stderr={sentinel.stderr!r}"
        )
    finally:
        # Power the guest off cleanly through devm, then reap whichever
        # `tart run` process is still attached to this VM name (the
        # recovery path replaces the raw-booted process with the
        # daemon's own StartVM-spawned one, so `proc` may already have
        # exited on its own by the time we get here).
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
