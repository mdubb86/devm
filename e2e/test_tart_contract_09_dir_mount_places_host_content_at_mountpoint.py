"""Pin: `tart run --dir=HOSTPATH:tag=TAG` + `mount -t virtiofs TAG MOUNTPOINT`
places host content directly at MOUNTPOINT inside the guest, and files on
the share appear in the guest owned by the default exec user (uid 1000 =
admin on cirruslabs/debian) regardless of the host uid that authored them.

Both are load-bearing for devm's workspace share:

  1. The mirrored-path decision (Ship 4) requires that host content at
     `<repoRoot>` surface at the SAME absolute path inside the guest. If
     tart's tag-based mount ever routed content to a subdirectory of the
     mountpoint, `<repoRoot>/.devm/scripts/*` would surface at
     `<repoRoot>/workspace/.devm/scripts/*` and every wrapper path in
     provision + orchestrator would silently miss.

  2. `internal/serviceapi/vminject.go:buildWorkspaceMountScript` intentionally
     drops the guest-side `chown admin:admin`. It does that because Apple
     Virtualization's virtiofs implementation already surfaces the share
     with admin ownership in the guest — a `chown admin:admin` is a no-op
     that hides the underlying invariant. If a future tart/macOS release
     stops remapping (files appearing as "nobody" / uid 65534 or the raw
     host uid like 501), the mount script needs an explicit chown or an
     fstab `uid=1000` mount option to give admin write access.
"""
from __future__ import annotations

import secrets
import subprocess
import tempfile
import time
from pathlib import Path

import pytest

from helpers import registry
from helpers.tart import TartSandbox


TEMPLATE = "ghcr.io/cirruslabs/debian:latest"
MOUNTPOINT = "/mnt/devm-workspace-pin"
MARKER_NAME = "pin-marker.txt"
MARKER_CONTENT = "hello-from-host"


@pytest.fixture
def mounted_workspace_vm():
    """Boot a fresh VM with an unnamed `--dir=HOSTPATH:tag=workspace` share.

    Yields (TartSandbox, host_share_path). VM is stopped + deleted on exit.
    """
    name = f"contract-mount-{secrets.token_hex(2)}"
    registry.append("sandbox", name)

    proc = None
    tmpdir_ctx = tempfile.TemporaryDirectory(prefix="devm-mount-pin-")
    host_share = Path(tmpdir_ctx.name)
    (host_share / MARKER_NAME).write_text(MARKER_CONTENT)

    try:
        subprocess.run(["tart", "pull", TEMPLATE], check=True, timeout=300)
        subprocess.run(["tart", "clone", TEMPLATE, name], check=True, timeout=60)

        # Unnamed --dir spec matches what serviceapi/vm.go emits for the
        # workspace share: --dir=HOSTPATH:tag=workspace.
        dir_arg = f"--dir={host_share}:tag=workspace"
        proc = subprocess.Popen(
            ["tart", "run", "--no-graphics", dir_arg, name],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )

        vm = TartSandbox(name=name)
        assert vm.wait_running(timeout=120), f"{name} never reached running"

        for _ in range(60):
            if vm.ip():
                break
            time.sleep(1)
        else:
            raise RuntimeError(f"{name} never got an IP")

        # Wait for the tart guest agent to accept exec connections.
        deadline = time.monotonic() + 60
        while time.monotonic() < deadline:
            if vm.exec("true").ok:
                break
            time.sleep(1)
        else:
            raise RuntimeError(f"{name} never became exec-ready")

        yield vm, host_share
    finally:
        subprocess.run(["tart", "stop", name], capture_output=True, timeout=30)
        if proc:
            try:
                proc.wait(timeout=30)
            except subprocess.TimeoutExpired:
                proc.kill()
        subprocess.run(["tart", "delete", name], capture_output=True, timeout=10)
        registry.remove("sandbox", name)
        tmpdir_ctx.cleanup()


@pytest.mark.contract
def test_unnamed_dir_share_mounts_host_content_at_mountpoint(mounted_workspace_vm):
    """--dir=PATH:tag=T + mount -t virtiofs T MP => host content visible at MP.

    If this ever fails with content appearing under MP/workspace/ instead,
    the mirrored-path assumption in devm's provisioner is broken.
    """
    vm, host_share = mounted_workspace_vm

    mount = vm.exec_shell(
        f"sudo mkdir -p {MOUNTPOINT} && "
        f"sudo mount -t virtiofs workspace {MOUNTPOINT}"
    )
    assert mount.ok, (
        f"guest mount failed: exit={mount.exit_code} "
        f"stderr={mount.stderr!r} stdout={mount.stdout!r}"
    )

    ls = vm.exec("ls", MOUNTPOINT)
    assert ls.ok, f"ls {MOUNTPOINT} failed: {ls.stderr!r}"
    assert MARKER_NAME in ls.stdout, (
        f"host marker missing from guest mountpoint. "
        f"host_share={host_share} guest_ls={ls.stdout!r} — "
        f"tart may be routing content under a subdirectory now"
    )

    marker = vm.exec("cat", f"{MOUNTPOINT}/{MARKER_NAME}")
    assert marker.ok, f"cat marker failed: {marker.stderr!r}"
    assert marker.stdout.strip() == MARKER_CONTENT, (
        f"marker content mismatch: {marker.stdout!r} != {MARKER_CONTENT!r}"
    )


@pytest.mark.contract
def test_share_files_appear_owned_by_admin_in_guest(mounted_workspace_vm):
    """Files on the share appear in the guest owned by admin (uid 1000).

    Pinned because `buildWorkspaceMountScript` drops the guest-side
    `chown admin:admin`. The chown is unnecessary because Apple
    Virtualization's virtiofs surfaces the share with admin ownership
    already — host uid 501 (macOS default) shows up as guest uid 1000.
    If that remapping ever changes (files appearing as "nobody" / 65534
    or the raw host uid), the mount script needs an explicit chown or a
    `uid=1000` fstab option to keep admin writable.
    """
    vm, _ = mounted_workspace_vm

    mount = vm.exec_shell(
        f"sudo mkdir -p {MOUNTPOINT} && "
        f"sudo mount -t virtiofs workspace {MOUNTPOINT}"
    )
    assert mount.ok, f"guest mount failed: {mount.stderr!r}"

    stat = vm.exec("stat", "-c", "%u:%g", f"{MOUNTPOINT}/{MARKER_NAME}")
    assert stat.ok, f"stat marker failed: {stat.stderr!r}"
    assert stat.stdout.strip() == "1000:1000", (
        f"virtiofs no longer surfaces share files as admin (uid 1000): "
        f"guest_stat={stat.stdout.strip()!r}. "
        f"buildWorkspaceMountScript needs to chown or add `uid=1000` to "
        f"the fstab entry so admin can still write the share."
    )
