"""Multi-repo config; each repo registers a startup command that reads a
file from its own guest cwd. All succeed.
"""
from __future__ import annotations
import subprocess
import pytest

pytestmark = pytest.mark.devm

SECONDARY_URL = "https://github.com/octocat/Spoon-Knife.git"
# schema.BareCloneName(SECONDARY_URL): strips owner + .git, so the
# guest clone lands at /home/devm/Spoon-Knife.
SECONDARY_LABEL = "Spoon-Knife"


@pytest.mark.timeout(400)
def test_multi_repo_startup_commands_see_hydrated(devm, workspace):
    workspace.write_devmyaml(
        repos={
            "main": {
                "url": workspace.bare_repo_url(),
                "primary": True,
                "commands": ["check-main"],
            },
            "secondary": {
                "url": SECONDARY_URL,
                "volume": True,
                "commands": ["check-secondary"],
            },
        },
    )
    workspace.write_devm_sh(
        "#!/usr/bin/env bash\n"
        "set -eo pipefail\n"
        "check-main() {\n"
        "  test -f README\n"
        "}\n"
        "check-secondary() {\n"
        "  test -f README.md\n"
        "}\n"
        "startup() {\n"
        f"  (cd /home/devm/{workspace.bare_repo_label()} && check-main)\n"
        f"  (cd /home/devm/{SECONDARY_LABEL} && check-secondary)\n"
        "}\n"
    )
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=300)
    assert r.returncode == 0, r.stderr.decode()
