"""Parse provisioning stage markers on stderr and assert sync fires
before install:.
"""
from __future__ import annotations
import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_stage_marker_order(devm, workspace):
    workspace.write_devmyaml(
        install=["echo install-ran"],
    )
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=180)
    assert r.returncode == 0, r.stderr.decode()

    stderr = r.stderr.decode()
    # Look for the new mutagen-sync marker (added in Task 8) before the
    # install marker. If the new marker isn't emitted (Task 8 chose not
    # to add a marker), fall back to asserting timing via daemon log grep.
    sync_pos = stderr.find("::devm:stage:mutagen-sync::")
    install_pos = stderr.find("::devm:stage:install::")
    assert sync_pos >= 0, f"mutagen-sync stage marker missing from:\n{stderr}"
    assert install_pos >= 0, f"install stage marker missing from:\n{stderr}"
    assert sync_pos < install_pos, \
        f"sync (pos {sync_pos}) must precede install (pos {install_pos})"
