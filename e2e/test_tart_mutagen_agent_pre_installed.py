"""Assert mutagen-agent is pre-installed in the guest at the expected path."""
from __future__ import annotations
import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_mutagen_agent_pre_installed(devm, workspace):
    workspace.write_devmyaml()
    r = subprocess.run(
        [devm.path, "start"], cwd=str(workspace.path),
        capture_output=True, timeout=180,
    )
    assert r.returncode == 0, r.stderr.decode()

    # Version-scoped path per mutagen's own convention.
    r = subprocess.run(
        [devm.path, "shell", "--", "stat", "-c", "%U:%G %a %s",
         "/home/devm/.mutagen/agents/0.18.1/mutagen-agent"],
        cwd=str(workspace.path), capture_output=True, timeout=60,
    )
    assert r.returncode == 0, r.stderr.decode()
    out = r.stdout.decode().strip()
    assert out.startswith("devm:devm 755 "), f"unexpected: {out}"
