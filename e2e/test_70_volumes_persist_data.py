"""70: a declared mount is accessible inside the VM and data persists across stop/restart.

The original sbx-era test (test_sbx_contract_15_kit_volumes_ext4) asserted
that a declared volume showed up as an ext4 block device via `findmnt -t ext4`.
That assertion cannot hold for Tart: virtio-fs shares are not ext4 block devices.

The meaningful behavior that still matters: the mount is present at the
declared path AND data written to it survives a `devm stop` + `devm shell`
restart cycle (i.e., the host-backed virtio-fs share provides persistence).

What this pins:
  - After cold-start with a declared mounts: entry, the path is a real
    mountpoint inside the VM (`mountpoint -q <path>` exits 0).
  - Data written to the mount from inside the VM is present on the host
    side of the virtio-fs share.
  - After `devm stop --yes` + `devm shell -- true` (VM restart), the data
    is still readable inside the VM.

What it doesn't cover (tested elsewhere):
  - :ro suffix enforcement -> test_59.
  - Mirrored path semantics (host path == guest path) -> test_58.
"""
from __future__ import annotations

import subprocess
import tempfile
import shutil
import time

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_mount_is_mountpoint_and_data_persists(workspace, devm, tart_sandbox):
    # Create a host-side directory that will be mounted as the "volume".
    vol_dir = tempfile.mkdtemp(prefix="devm-e2e-vol70-")
    try:
        workspace.write_devmyaml(
            mounts=[vol_dir],
        )

        assert tart_sandbox.state() == "running", (
            f"expected VM running after cold-start; got {tart_sandbox.state()!r}"
        )

        # The mount path must be a real mountpoint inside the VM.
        r = tart_sandbox.exec("mountpoint", "-q", vol_dir)
        assert r.exit_code == 0, (
            f"declared mount {vol_dir!r} is not a mountpoint inside the VM "
            f"(exit_code={r.exit_code}, stderr={r.stderr!r})"
        )

        # Write a probe file to the mount from inside the VM.
        probe_path = f"{vol_dir}/probe-vol70"
        r = tart_sandbox.exec_shell(f"echo persist > {probe_path}")
        assert r.exit_code == 0, (
            f"failed to write probe file inside VM: {r.stderr!r}"
        )

        # Sanity: readable immediately.
        r = tart_sandbox.exec_shell(f"cat {probe_path}")
        assert r.exit_code == 0 and r.stdout.strip() == "persist", (
            f"probe file not immediately readable: exit={r.exit_code} out={r.stdout!r}"
        )

        # Stop the VM.
        devm.stop(yes=True, timeout=30)

        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if tart_sandbox.state() == "stopped":
                break
            time.sleep(0.5)
        assert tart_sandbox.state() == "stopped", (
            f"VM should be stopped after devm stop; got {tart_sandbox.state()!r}"
        )

        # Restart via devm shell -- true.
        subprocess.run(
            [devm.path, "shell", "--", "true"],
            capture_output=True, cwd=str(workspace.path), timeout=180,
        )

        deadline = time.monotonic() + 30
        while time.monotonic() < deadline:
            if tart_sandbox.state() == "running":
                break
            time.sleep(0.5)
        assert tart_sandbox.state() == "running", (
            f"VM should be running after restart; got {tart_sandbox.state()!r}"
        )

        # Probe file must survive the stop/restart cycle.
        r = tart_sandbox.exec_shell(f"cat {probe_path}")
        assert r.exit_code == 0, (
            f"probe file missing after stop/restart — mount data did not persist: "
            f"exit={r.exit_code} stderr={r.stderr!r}"
        )
        assert r.stdout.strip() == "persist", (
            f"probe file content corrupted after stop/restart: {r.stdout!r}"
        )
    finally:
        shutil.rmtree(vol_dir, ignore_errors=True)
