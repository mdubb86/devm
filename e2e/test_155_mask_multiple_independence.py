"""155: multiple masks are independent.

Two masks declared; write different content into each; verify each
storage dir has only its own content. Then live-remove one, verify
the other is still mounted and functional.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_mask_multiple_independence(devm, workspace, sandbox_name):
    workspace.write_devmyaml(
        install=["true"],
        masks=["dir_a", "dir_b"],
    )
    try:
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        r = subprocess.run(
            [devm.path, "shell", "--", "sh", "-c",
             f"echo a-content > {workspace.path}/dir_a/file && "
             f"echo b-content > {workspace.path}/dir_b/file"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"writes failed:\n{r.stderr.decode()}"

        # Each storage dir has only its own content.
        r = subprocess.run(
            [devm.path, "shell", "--", "cat",
             f"/var/devm/masks/{workspace.slug}/dir_a/file"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0
        assert r.stdout.decode().strip() == "a-content"

        r = subprocess.run(
            [devm.path, "shell", "--", "cat",
             f"/var/devm/masks/{workspace.slug}/dir_b/file"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0
        assert r.stdout.decode().strip() == "b-content"

        # Live-remove dir_a; dir_b must still be mounted.
        devm.unlock()
        workspace.patch_devmyaml(install=["true"], masks=["dir_b"])
        r = subprocess.run(
            [devm.path, "reconcile", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0

        # dir_b still mounted.
        r = subprocess.run(
            [devm.path, "shell", "--", "sh", "-c",
             f"mountpoint -q {workspace.path}/dir_b && echo mounted || echo NOT_MOUNTED"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0
        assert "mounted" in r.stdout.decode()

        # dir_a NOT mounted anymore.
        r = subprocess.run(
            [devm.path, "shell", "--", "sh", "-c",
             f"mountpoint -q {workspace.path}/dir_a && echo mounted || echo NOT_MOUNTED"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0
        assert "NOT_MOUNTED" in r.stdout.decode()
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
