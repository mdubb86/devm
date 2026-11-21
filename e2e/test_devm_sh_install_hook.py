"""devm.sh install() magic function fires at the install phase, gets the
same env install: entries get today, and its non-zero exit fails cold-start.
"""
from __future__ import annotations
import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_install_hook_runs_and_env_is_present(workspace, devm):
    workspace.write_devmyaml(env={"HELLO": "world"})
    workspace.write_devm_sh(
        "#!/usr/bin/env bash\n"
        "set -eo pipefail\n"
        "install() {\n"
        '  echo "HELLO=${HELLO:-<unset>}" > /home/devm/install-marker\n'
        "}\n"
    )
    r = subprocess.run(
        [devm.path, "start"], cwd=str(workspace.path),
        capture_output=True, timeout=180,
    )
    assert r.returncode == 0, r.stderr.decode()

    got = subprocess.run(
        [devm.path, "shell", "--", "cat", "/home/devm/install-marker"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert got.returncode == 0
    assert "HELLO=world" in got.stdout.decode()
