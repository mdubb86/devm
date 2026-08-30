"""158: `packages:` live add/remove converges apt on a running VM
inside a transient egress window; a stopped-VM edit converges on the
next boot without a full recreate.

`packages:` is a LIVE-bucket field (like `env:`/`path:` in test_11):
an explicit `devm reconcile --yes` against a running VM installs/
removes the diffed packages via a transient apt-egress-window respawn
of iron-proxy (apt's mirror hosts allowed only for the duration of the
apt run, restored immediately after — see
`serviceapi.realPackagesApplier.ApplyPackages`), with no VM cycle and
no teardown-required prompt (packages changes never land in
RecreateRequired).

A stopped VM's `devm.yaml` edit isn't applied by a reconcile call —
it's picked up by the next boot's provisioning window
(`render.RenderProvisionOpenScript`'s non-first-boot `PackageAdds`/
`PackageRemoves` converge stage), same as `install:`'s first-boot-only
stage is skipped on restart (test_88).

Package choice: `sl` — tiny, no dependencies, and (like test_76's
`jq`) trivially verifiable via `which`.

Flow:
  1. Cold-start with no `packages:`. Record boot id + plant a marker
     file under /home/devm (test_52/test_88's pattern) that a later
     recreate would destroy but a restart would not.
  2. LIVE add (`packages: [sl]` + `devm reconcile --yes`): assert
     `+ package sl` in stdout, no recreate-required prompt, `sl` on
     PATH, boot id unchanged, and the apt-egress window closed (an
     apt mirror host is back to steady-state 403 after the apply).
  3. LIVE remove (`packages: []` + `devm reconcile --yes`): assert
     `- package sl` in stdout, `sl` gone, boot id still unchanged.
  4. Stopped-VM path: `devm stop`, edit to `packages: [sl]`, `devm
     shell -- which sl` — succeeds (converged during the boot's open
     window). Boot id changed (a real boot happened) but the marker
     from step 1 survived (disk reused, not recreated).

What this pins:
  - `packages:` add/remove reconciles live on a running VM, no VM
    cycle, no teardown-required prompt.
  - The apt-converge egress window is genuinely transient — closed
    again once the apply returns.
  - A stopped VM's package edit converges on the next boot via the
    non-first-boot stage, without a full recreate.

What it doesn't cover (tested elsewhere):
  - `packages:` at cold-start (first-boot stage) -> test_76.
  - `network.allow` add/remove live-reconcile mechanics -> test_92,
    test_131.
  - `install:` never re-running on restart -> test_88.
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers import stop_and_wait_stopped
from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm

MARKER = "/home/devm/.e2e-packages-live-marker"


def _boot_id(devm_path: str, cwd: str) -> str:
    r = devm_exec_with_retry(
        devm_path, ["cat", "/proc/sys/kernel/random/boot_id"],
        cwd=cwd, timeout=30,
    )
    assert r.returncode == 0, (
        f"reading boot_id failed:\nstdout={r.stdout.decode()!r}\n"
        f"stderr={r.stderr.decode()!r}"
    )
    return r.stdout.decode().strip()


def _which_sl(devm_path: str, cwd: str) -> subprocess.CompletedProcess:
    return subprocess.run(
        [devm_path, "exec", "which", "sl"],
        cwd=cwd, capture_output=True, timeout=30,
    )


def _apt_host_status(devm_path: str, cwd: str) -> str:
    """HTTP status code iron-proxy returns for deb.debian.org right now."""
    r = devm_exec_with_retry(
        devm_path,
        ["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
         "--max-time", "15", "https://deb.debian.org/"],
        cwd=cwd, timeout=30,
    )
    assert r.returncode == 0, (
        f"curl to deb.debian.org failed to run:\nstdout={r.stdout.decode()!r}\n"
        f"stderr={r.stderr.decode()!r}"
    )
    return r.stdout.decode().strip().splitlines()[-1]


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_packages_live_add_remove(workspace, devm, sandbox_name):
    # config_lock: false so the mid-test devm.yaml edits against the
    # running VM don't need `devm unlock` each time (test_148's
    # pattern) -- config-lock's own coverage is test_120.
    workspace.write_devmyaml(config_lock=False)

    try:
        # 1. Cold-start with no packages:.
        cold = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert cold.returncode == 0, (
            f"cold-start failed:\nstdout={cold.stdout.decode()!r}\n"
            f"stderr={cold.stderr.decode()!r}"
        )

        boot_id_0 = _boot_id(devm.path, str(workspace.path))

        # Plant a disk marker before the eventual `devm stop` in step
        # 4 -- its survival across stop -> edit -> boot proves that
        # boot reused the disk (restart) instead of tearing down and
        # cloning a fresh one (recreate).
        plant = devm_exec_with_retry(
            devm.path, ["sh", "-c", f"echo hello > {MARKER} && sync"],
            cwd=str(workspace.path), timeout=15,
        )
        assert plant.returncode == 0, f"planting marker failed: {plant.stderr.decode()!r}"

        before = _which_sl(devm.path, str(workspace.path))
        assert before.returncode != 0, "sl should not be installed at cold-start"

        # 2. LIVE add: packages: [sl]. `devm reconcile --yes` converges
        # the running VM inside a transient apt-egress window.
        workspace.patch_devmyaml(packages=["sl"])
        add = devm.reconcile(yes=True, timeout=240)
        add_out = add.stdout.decode()
        assert add.returncode == 0, (
            f"reconcile (add) failed:\nstdout={add_out!r}\n"
            f"stderr={add.stderr.decode()!r}"
        )
        assert "+ package sl" in add_out, (
            f"expected '+ package sl' in reconcile output: {add_out!r}"
        )
        assert "recreate" not in add_out.lower(), (
            f"a packages: live add must never trigger the teardown/recreate "
            f"prompt: {add_out!r}"
        )

        after_add = _which_sl(devm.path, str(workspace.path))
        assert after_add.returncode == 0, (
            f"sl not on PATH after live add:\nstdout={after_add.stdout.decode()!r}\n"
            f"stderr={after_add.stderr.decode()!r}"
        )

        boot_id_after_add = _boot_id(devm.path, str(workspace.path))
        assert boot_id_after_add == boot_id_0, (
            "boot id changed after a live packages add -- the VM was cycled "
            "instead of converged in place"
        )

        # The apt-egress window is transient: ApplyPackages restores the
        # steady-state allowlist before the reconcile call even returns,
        # so deb.debian.org should already be back to a 403 deny. Poll
        # briefly as a guard against any propagation lag.
        deadline = time.monotonic() + 20
        status = None
        while time.monotonic() < deadline:
            status = _apt_host_status(devm.path, str(workspace.path))
            if status == "403":
                break
            time.sleep(1)
        assert status == "403", (
            f"deb.debian.org still reachable ({status}) after the apt-converge "
            f"window should have closed -- the transient widen wasn't restored"
        )

        # 3. LIVE remove: back to packages: [].
        workspace.patch_devmyaml(packages=[])
        remove = devm.reconcile(yes=True, timeout=180)
        remove_out = remove.stdout.decode()
        assert remove.returncode == 0, (
            f"reconcile (remove) failed:\nstdout={remove_out!r}\n"
            f"stderr={remove.stderr.decode()!r}"
        )
        assert "- package sl" in remove_out, (
            f"expected '- package sl' in reconcile output: {remove_out!r}"
        )

        after_remove = _which_sl(devm.path, str(workspace.path))
        assert after_remove.returncode != 0, "sl should be gone after live remove"

        boot_id_after_remove = _boot_id(devm.path, str(workspace.path))
        assert boot_id_after_remove == boot_id_0, (
            "boot id changed after a live packages remove -- the VM was "
            "cycled instead of converged in place"
        )

        # 4. Stopped-VM path: stop, edit to packages: [sl] while
        # stopped, then the next `devm shell` converges packages during
        # its boot's open window -- no reconcile call needed.
        stop_and_wait_stopped(devm, sandbox_name)
        workspace.patch_devmyaml(packages=["sl"])

        boot = subprocess.run(
            [devm.path, "shell", "--", "which", "sl"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert boot.returncode == 0, (
            f"stopped-VM converge failed:\nstdout={boot.stdout.decode()!r}\n"
            f"stderr={boot.stderr.decode()!r}"
        )

        boot_id_after_restart = _boot_id(devm.path, str(workspace.path))
        assert boot_id_after_restart != boot_id_0, (
            "expected a NEW boot id after `devm stop` + `devm shell` -- a "
            "real boot should have happened"
        )

        # Marker planted in step 1 is still there: this boot reused the
        # disk (restart + non-first-boot converge), it didn't tear down
        # and recreate the VM.
        marker_check = devm_exec_with_retry(
            devm.path, ["test", "-f", MARKER],
            cwd=str(workspace.path), timeout=15,
        )
        assert marker_check.returncode == 0, (
            "marker file planted before `devm stop` is gone -- the VM was "
            "recreated instead of restarted for the stopped-VM packages edit"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
