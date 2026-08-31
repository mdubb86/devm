"""178: adding a secondary repo (`repos:` with volume: true) via
`devm reconcile --yes`.

Adding a `repos:` entry with volume: true is a change that requires the
VM to be restarted (the mutagen session and its Mac mirror have to be
allocated fresh). reconcile detects the diff, tears down + cold-starts
with the new entity declared, and the secondary clone lands in its
guest path on the fresh boot.
"""
from __future__ import annotations
import subprocess

import pytest

pytestmark = pytest.mark.devm

SECONDARY_URL = "https://github.com/octocat/Spoon-Knife.git"


@pytest.mark.timeout(400)
def test_reconcile_adds_secondary_volume(devm, workspace):
    workspace.write_devmyaml()   # fixture default: primary Hello-World

    try:
        # Initial cold-start: primary only.
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"initial cold-start failed:\n{r.stderr.decode()}"

        # Secondary not present yet.
        r = subprocess.run(
            [devm.path, "shell", "--", "test", "-d", "/home/devm/Spoon-Knife"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode != 0, "/home/devm/Spoon-Knife should not exist before reconcile"

        # Add a secondary repo with volume: true; reconcile picks it up.
        workspace.patch_devmyaml(
            repos={
                "main": {
                    "url": workspace.bare_repo_url(),
                    "primary": True,
                },
                "secondary": {
                    "url": SECONDARY_URL,
                    "volume": True,
                },
            },
        )

        result = devm.reconcile(yes=True, timeout=300)
        assert result.returncode == 0, result.stderr.decode()

        r = subprocess.run(
            [devm.path, "shell", "--", "ls", "/home/devm/Spoon-Knife"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert "index.html" in r.stdout.decode(), (
            f"secondary Spoon-Knife not hydrated after reconcile; got:\n{r.stdout.decode()}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
