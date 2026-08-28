"""240: internal/mutagen.DefaultIgnores are honored by a live sync --
a `node_modules/` file written in the guest never reaches the Mac
mirror.

A sibling non-ignored file is written in the same flush to prove sync
is actually running (a "didn't appear" assertion alone can't
distinguish "correctly ignored" from "sync is broken").
"""
from __future__ import annotations
import subprocess

import pytest

from helpers.mutagen_e2e import mirror_path, session_prefix, sync_flush, sync_list

pytestmark = pytest.mark.devm


@pytest.mark.timeout(240)
def test_default_ignores_apply(devm, workspace):
    workspace.write_devmyaml()  # default repos.main only
    label = f"{workspace.path.name}-repo"

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

        sessions = sync_list(session_prefix(workspace.vm_name))
        assert len(sessions) == 1, f"expected exactly one session, got {sessions}"
        session_id = sessions[0]["identifier"]

        script = (
            f"mkdir -p /home/devm/{label}/node_modules\n"
            f"echo ignored > /home/devm/{label}/node_modules/foo.txt\n"
            f"echo tracked > /home/devm/{label}/normal.txt\n"
        )
        r = subprocess.run(
            [devm.path, "shell", "--", "sh", "-c", script],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"guest write failed:\n{r.stderr.decode()}"

        r = sync_flush(session_id)
        assert r.returncode == 0, f"mutagen sync flush failed:\n{r.stderr}"

        mirror = mirror_path(workspace.vm_name, label)
        assert not (mirror / "node_modules" / "foo.txt").exists(), (
            f"node_modules/foo.txt should be ignored by the default "
            f"'**/node_modules/' pattern but appeared at {mirror}"
        )
        assert (mirror / "normal.txt").exists(), (
            f"normal.txt (not covered by any ignore pattern) should have "
            f"synced to the Mac mirror at {mirror} -- if it's missing, sync "
            f"itself may be broken rather than the ignore rule working"
        )
        assert (mirror / "normal.txt").read_text().strip() == "tracked"
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
