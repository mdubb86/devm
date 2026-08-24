"""175: volume storage naming — primary uses the Mac cwd basename,
secondaries use their `volumes:` map key.

`devm volume ls` prints each volume's name and Mac-side storage path,
so it doubles as a direct probe of the daemon's actual naming
decision (not just the test fixture's presumed formula).
"""
from __future__ import annotations
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_storage_naming_primary_and_secondary(devm, workspace):
    url = workspace.bare_repo_url()
    workspace.write_devmyaml(
        volumes={
            "mydata": {
                "path": "/mnt/mydata",
                "repo": {"url": url, "secret": "e2e_default"},
            },
        },
    )

    try:
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # Primary: named after the Mac cwd basename.
        primary_name = workspace.path.name
        primary_storage = workspace.volume_path()
        assert primary_storage.name == primary_name
        assert (primary_storage / "README.md").exists()

        # Secondary: named after its `volumes:` map key ("mydata"),
        # NOT some transformed/prefixed form.
        secondary_storage = workspace.volume_path("mydata")
        assert secondary_storage.name == "mydata"
        assert (secondary_storage / "README.md").exists()

        r = subprocess.run(
            [devm.path, "volume", "ls"],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert r.returncode == 0, r.stderr.decode()
        out = r.stdout.decode()
        assert primary_name in out
        assert str(primary_storage) in out
        assert "mydata" in out
        assert str(secondary_storage) in out
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
