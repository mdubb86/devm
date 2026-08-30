"""158: `packages:` live add/remove converges apt on a running VM under
the project's CURRENT egress policy; a stopped-VM edit converges on the
next boot without a full recreate.

`packages:` is a LIVE-bucket field (like `env:`/`path:` in test_11): an
explicit `devm reconcile --yes` against a running VM installs/removes
the diffed packages via `serviceapi.realPackagesApplier.ApplyPackages`,
with no VM cycle and no teardown-required prompt (packages changes never
land in RecreateRequired). ApplyPackages runs the apt converge script
under exactly the allowlist the user wrote in `network.allow` — there is
no automatic widen/restore of the Debian mirrors (retired; see
apply_packages.go). A user whose allow-list doesn't cover
`deb.debian.org` / `security.debian.org` gets a loud failure naming both
fixes (`aptEgressHint`), and the blocked mirrors show up in
`devm denials` same as any other rejected host.

A stopped VM's `devm.yaml` edit isn't applied by a reconcile call — it's
picked up by the next boot's provisioning window
(`render.RenderProvisionOpenScript`'s non-first-boot `PackageAdds`/
`PackageRemoves` converge stage), same as `install:`'s first-boot-only
stage is skipped on restart (test_88).

Package choice: `sl` — tiny, no dependencies, and (like test_76's `jq`)
trivially verifiable via `which`.

What this pins:
  - `packages:` add/remove reconciles live on a running VM, no VM cycle,
    no teardown-required prompt, when `network.allow` already covers
    the Debian mirrors.
  - A stopped VM's package edit converges on the next boot via the
    non-first-boot stage, without a full recreate.
  - Without the mirrors in `network.allow`, a live packages add fails
    loud with devm's egress hint, installs nothing, and `devm denials`
    shows the blocked mirrors.

What it doesn't cover (tested elsewhere):
  - `packages:` at cold-start (first-boot stage) -> test_76.
  - `network.allow` add/remove live-reconcile mechanics -> test_92,
    test_131, test_230.
  - `install:` never re-running on restart -> test_88.
"""
from __future__ import annotations

import json
import subprocess

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


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_packages_live_add_remove(workspace, devm, sandbox_name):
    # Fixture's own network.allow covers the Debian mirrors so the live
    # add below converges under the project's current (steady-state)
    # policy -- no automatic widen/restore.
    workspace.write_devmyaml(
        no_repo=True,
        network={"allow": ["deb.debian.org", "security.debian.org"]},
    )

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
        # the running VM under the current allowlist (which already
        # covers the Debian mirrors).
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


@pytest.mark.slow
@pytest.mark.timeout(300)
def test_packages_live_add_fails_loud_without_mirror_allow(workspace, devm, sandbox_name):
    """Without the Debian mirrors in network.allow, a live packages add
    fails loud: apt hits devm's self-describing 403s, the reconcile
    error carries realPackagesApplier's aptEgressHint naming both fixes,
    nothing gets installed, and `devm denials` shows the blocked
    mirrors."""
    workspace.write_devmyaml(
        no_repo=True,
        network={"allow": ["example.com"]},
    )

    try:
        cold = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert cold.returncode == 0, (
            f"cold-start failed:\nstdout={cold.stdout.decode()!r}\n"
            f"stderr={cold.stderr.decode()!r}"
        )

        workspace.patch_devmyaml(packages=["sl"])
        add = devm.reconcile(yes=True, timeout=120, check=False)
        out = add.stdout.decode() + add.stderr.decode()
        assert add.returncode != 0, (
            f"a live packages add without the Debian mirrors allowlisted "
            f"should fail loud: {out!r}"
        )
        assert "apt egress may be blocked by network.allow" in out, (
            f"expected devm's egress hint in the reconcile failure: {out!r}"
        )

        p = subprocess.run(
            [devm.path, "denials", "--json"],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert p.returncode == 0, f"devm denials failed:\n{p.stderr.decode()}"
        hosts = {row["host"] for row in json.loads(p.stdout.decode())}
        assert "deb.debian.org" in hosts, (
            f"expected deb.debian.org among the denied hosts; got {hosts}"
        )
        # security.debian.org is deliberately NOT asserted here: the guest's
        # apt sources route security updates through deb.debian.org's
        # /debian-security mirrorlist path, not the separate
        # security.debian.org host, so apt never dials it -- and under
        # `bash -e` the converge script aborts as soon as deb.debian.org's
        # 403 fails `apt-get update`, before any other source is tried.
        # The aptEgressHint still names both hosts (it's a static remedy
        # string, not a report of what was actually dialed), so that
        # assertion is unaffected.

        which = _which_sl(devm.path, str(workspace.path))
        assert which.returncode != 0, (
            "sl should not be installed -- the failed apply must not have "
            "left a half-applied package"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
