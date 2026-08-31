"""150: cold-start with a mask declared.

Simplest end-to-end proof of the top-level mask contract. Cold-start
with `masks: [scratch]`, write a sentinel file into the workspace's
scratch path (which is mask-mounted), verify the file lives on the
guest ext4 backing dir at /var/devm/masks/<project>/scratch/, not
on the Mac side of the workspace.

If this fails: either the daemon didn't render the mask into the
provisioning script OR the guest mount script didn't bind the
storage path over the workspace target OR the storage-path shape
regressed to include a <service>/ component.
"""
from __future__ import annotations

import os
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_mask_cold_start(devm, workspace, sandbox_name):
    workspace.write_devmyaml(
        install=["true"],
        masks=["scratch"],
    )
    try:
        # Cold-start.
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # Write a sentinel via the workspace target (which is bind-mounted
        # to the guest-ext4 backing dir).
        r = subprocess.run(
            [devm.path, "shell", "--", "sh", "-c",
             f"echo mask-content > {workspace.path}/scratch/sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"sentinel write failed:\n{r.stderr.decode()}"

        # Verify the sentinel is at the storage path in the guest.
        r = subprocess.run(
            [devm.path, "shell", "--", "cat",
             f"/var/devm/masks/{workspace.slug}/scratch/sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, (
            f"reading via storage path failed:\n"
            f"stdout: {r.stdout.decode()!r}\n"
            f"stderr: {r.stderr.decode()!r}\n"
            f"If the storage path doesn't exist, the daemon didn't render "
            f"the mask into provisioning. If it exists but is empty, the "
            f"bind mount didn't take (the workspace-side path may still be "
            f"pointing at Mac content)."
        )
        assert r.stdout.decode().strip() == "mask-content"

        # Verify the sentinel is NOT on the Mac side of the workspace.
        mac_side = os.path.join(str(workspace.path), "scratch", "sentinel")
        assert not os.path.exists(mac_side), (
            f"mask leaked to Mac side: {mac_side} exists — the bind mount "
            f"is not overlaying correctly, or the mask isn't declared."
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
