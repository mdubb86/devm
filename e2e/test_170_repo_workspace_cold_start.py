"""170: primary-only repo workspace hydrates on cold-start.

Pins:
1. Cold-start with `repos.main:` clones into the guest at
   `/home/devm/<label>/` (label defaults to the bare-clone name derived
   from the URL -- the fixture's default `repos:` block points at a
   hermetic local `file://` bare repo).
2. The guest sees the cloned content there.
3. After a `mutagen sync flush`, the Mac-side mirror at
   `~/Library/Application Support/devm-e2e/<project>/<label>/` is
   populated with the same content -- proof the mutagen session is
   actually wired up, not just that the guest-side clone ran.

There's no `.vm` symlink and no live bind mount at the Mac cwd: the
guest path and the Mac cwd are independent, connected only by
mutagen's two-way sync.
"""
from __future__ import annotations
import subprocess

import pytest

from helpers.mutagen_e2e import mirror_path, session_prefix, sync_flush, sync_list

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_repo_workspace_cold_start(devm, workspace, sandbox_name):
    workspace.write_devmyaml()  # fixture's default repos.main is enough
    label = workspace.bare_repo_label()

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, r.stderr.decode()

        # Guest sees the cloned content at /home/devm/<label>.
        # Hello-World ships a `README` (no extension).
        r = subprocess.run(
            [devm.path, "shell", "--", "ls", f"/home/devm/{label}"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert "README" in r.stdout.decode(), (
            f"expected README in guest tree; got:\n{r.stdout.decode()}"
        )

        # Mac mirror populated after a flush.
        sessions = sync_list(session_prefix(workspace.vm_name))
        assert len(sessions) == 1, f"expected exactly one session, got {sessions}"
        r = sync_flush(sessions[0]["identifier"])
        assert r.returncode == 0, f"mutagen sync flush failed:\n{r.stderr}"

        mirror = mirror_path(workspace.vm_name, label)
        assert (mirror / "README").exists(), (
            f"primary Mac mirror {mirror} missing cloned README"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
