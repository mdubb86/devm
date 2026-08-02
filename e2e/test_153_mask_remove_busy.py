"""153: live-remove mask with a service holding a file open → clear error.

Starts a `sleep infinity`-style service that holds the mask directory
open (e.g. via `tail -f /dev/null > /workspace/scratch/live &` from a
persistent service). Then removes the mask from devm.yaml and runs
reconcile. Reconcile must abort with the spec's exact error message
naming the mount point.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_mask_remove_ebusy_errors_clearly(devm, workspace, sandbox_name):
    # Service that holds a shell open with cwd inside the masked path,
    # so umount returns EBUSY (chdir into a mounted dir counts as a
    # use).
    workspace.write_devmyaml(
        install=["true"],
        masks=["scratch"],
        services={
            "holder": {
                "exec": ["/bin/sh", "-c", f"cd {workspace.path}/scratch && sleep infinity"],
                "restart": "always",
            },
        },
    )
    try:
        # Cold-start with mask + service running inside it.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # Confirm the holder is cd'd into the mask target (its cwd will
        # be the target path, which is proof it's holding the mount).
        r = subprocess.run(
            [devm.path, "shell", "--", "sh", "-c",
             "sleep 2; pgrep -af 'sleep infinity'"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, "holder service didn't start"

        # Try to live-remove the mask — should error with EBUSY message.
        devm.unlock()
        workspace.patch_devmyaml(
            install=["true"],
            services={
                "holder": {
                    "exec": ["/bin/sh", "-c", f"cd {workspace.path}/scratch && sleep infinity"],
                    "restart": "always",
                },
            },
        )
        r = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode != 0, (
            "reconcile succeeded but should have failed with EBUSY"
        )
        stderr = r.stderr.decode()
        assert "cannot unmount mask `scratch`" in stderr, (
            f"missing EBUSY error header:\n{stderr}"
        )
        assert "is in use" in stderr, f"missing 'in use' language:\n{stderr}"
        assert "devm shell" in stderr, f"missing user remediation hint:\n{stderr}"
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
