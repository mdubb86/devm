"""Prove workspace is hydrated by the time install: runs.

install: reads a file placed via mutagen; if the workspace weren't
hydrated, install: would fail.
"""
from __future__ import annotations
import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_install_sees_hydrated_workspace(devm, workspace):
    workspace.write_devmyaml()
    workspace.write_devm_sh(
        "#!/usr/bin/env bash\n"
        "set -eo pipefail\n"
        "install() {\n"
        "  test -f $WORKSPACE/README && echo install-saw-workspace\n"
        "}\n"
    )
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=180)
    assert r.returncode == 0, r.stderr.decode()
