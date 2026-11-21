"""Prove startup:true commands see open egress; the same command run
manually is enforced. Asymmetric egress model is real and documented.
"""
from __future__ import annotations
import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_startup_open_manual_enforced(devm, workspace):
    workspace.write_devmyaml(
        repos={
            "main": {
                "url": workspace.bare_repo_url(),
                "primary": True,
                "commands": ["fetch"],
            },
        },
    )
    # pypi.org is not on network.allow (default allow is github.com
    # only): open during startup, enforced for a manual run below.
    workspace.write_devm_sh(
        "#!/usr/bin/env bash\n"
        "set -eo pipefail\n"
        "fetch() {\n"
        "  curl -sSf --max-time 10 https://pypi.org/simple/ > /dev/null\n"
        "}\n"
        "startup() {\n"
        f"  (cd /home/devm/{workspace.bare_repo_label()} && fetch)\n"
        "}\n"
    )
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=180)
    assert r.returncode == 0, \
        f"startup: with open egress should succeed:\n{r.stderr.decode()}"

    # Manual run of same command SHOULD fail (enforced egress).
    r = subprocess.run(
        [devm.path, "shell", "--", "run", "fetch"],
        cwd=str(workspace.path), capture_output=True, timeout=60,
    )
    assert r.returncode != 0, \
        "manual run under enforced egress should fail without allowlist"
