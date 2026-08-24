"""178: adding a secondary repo volume via `devm reconcile --yes`.

A `volumes:` map change is a KindVolumeChange diff, bucketed
BucketRestartVM: it can't be applied live (tart takes --dir args only
at VM launch), so reconcile stops the VM and cold-starts it again with
the new volume declared. Config-lock holds devm.yaml host-immutable
while the VM runs, so the edit needs `devm unlock` first (same
sequencing as test_09/test_120's config-lock contract).
"""
from __future__ import annotations
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(400)
def test_reconcile_adds_secondary_volume(devm, workspace):
    url = workspace.bare_repo_url()
    workspace.write_devmyaml()

    try:
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"initial cold-start failed:\n{r.stderr.decode()}"

        # Secondary not present yet.
        r = subprocess.run(
            [devm.path, "shell", "--", "test", "-d", "/mnt/secondary"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode != 0, "/mnt/secondary should not exist before reconcile"

        devm.unlock()
        workspace.patch_devmyaml(
            volumes={
                "secondary": {
                    "path": "/mnt/secondary",
                    "repo": {"url": url, "secret": "e2e_default"},
                },
            },
        )

        result = devm.reconcile(yes=True, timeout=300)
        assert result.returncode == 0, result.stderr.decode()

        r = subprocess.run(
            [devm.path, "shell", "--", "ls", "/mnt/secondary"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert "README.md" in r.stdout.decode(), "secondary volume not hydrated after reconcile"
        assert (workspace.volume_path("secondary") / "README.md").exists()
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
