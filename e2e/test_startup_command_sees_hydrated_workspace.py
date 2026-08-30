"""Multi-repo config; each repo's `commands.<name>.startup: true` reads a
file from its own guest cwd. All succeed.
"""
from __future__ import annotations
import subprocess
import pytest

pytestmark = pytest.mark.devm

SECONDARY_URL = "https://github.com/octocat/Spoon-Knife.git"


@pytest.mark.timeout(400)
def test_multi_repo_startup_commands_see_hydrated(devm, workspace):
    workspace.write_devmyaml(
        repos={
            "main": {
                "url": workspace.bare_repo_url(),
                "primary": True,
                "commands": {
                    "check": {"exec": "test -f README", "startup": True},
                },
            },
            "secondary": {
                "url": SECONDARY_URL,
                "volume": True,
                "commands": {
                    "check": {"exec": "test -f README.md", "startup": True},
                },
            },
        },
    )
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=300)
    assert r.returncode == 0, r.stderr.decode()
