"""Mutagen-setup failure in its new position = teardown-class failure,
same as install:. Parallel to test_51_lifecycle_install_failure_surfaces_loud.
"""
from __future__ import annotations
import json
import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_mutagen_setup_failure_tears_down(devm, workspace):
    workspace.write_devmyaml(
        repos={
            "main": {
                # Guaranteed-unreachable URL.
                "url": "https://example.invalid/bogus/repo.git",
                "primary": True,
            },
        },
    )
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=180)
    assert r.returncode != 0, "start with unreachable repo must fail"

    # State should be absent (teardown class): VM is torn down, not
    # left running.
    r = subprocess.run([devm.path, "status", "--json"],
                       cwd=str(workspace.path), capture_output=True, timeout=30)
    assert r.returncode == 0, f"devm status failed:\n{r.stderr.decode()}"
    data = json.loads(r.stdout)
    state = (data.get("project") or {}).get("state")
    assert state == "absent", f"expected teardown; got state={state!r}"
