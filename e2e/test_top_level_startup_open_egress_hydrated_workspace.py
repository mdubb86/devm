"""Prove top-level startup: (1) sees open egress AND (2) sees hydrated
workspace, in one assertion pair.
"""
from __future__ import annotations
import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_top_level_startup_combined_properties(devm, workspace):
    workspace.write_devmyaml()
    workspace.write_devm_sh(
        "#!/usr/bin/env bash\n"
        "set -eo pipefail\n"
        "startup() {\n"
        "  test -f $WORKSPACE/README\n"  # hydrated
        "  curl -sSf --max-time 10 https://pypi.org/simple/ > /dev/null\n"  # open egress
        "}\n"
    )
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=180)
    assert r.returncode == 0, r.stderr.decode()
