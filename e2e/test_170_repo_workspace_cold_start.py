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
4. A `commands.<name>.startup: true` command fires post-hydration and
   its output is visible both in the guest and, after a flush, in the
   Mac mirror.

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
    label = workspace.bare_repo_label()
    workspace.write_devmyaml(
        repos={
            "main": {
                "url": workspace.bare_repo_url(),
                "primary": True,
                "commands": {
                    "install": {
                        "exec": "echo hi > $WORKSPACE/startup-sentinel",
                        "startup": True,
                    },
                },
            },
        },
    )

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

        # Startup command fired post-hydration.
        r = subprocess.run(
            [devm.path, "shell", "--", "cat", f"/home/devm/{label}/startup-sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert "hi" in r.stdout.decode()

        # Mac mirror populated after a flush.
        sessions = sync_list(session_prefix(workspace.vm_name))
        assert len(sessions) == 1, f"expected exactly one session, got {sessions}"
        r = sync_flush(sessions[0]["identifier"])
        assert r.returncode == 0, f"mutagen sync flush failed:\n{r.stderr}"

        mirror = mirror_path(workspace.vm_name, label)
        assert (mirror / "README").exists(), (
            f"primary Mac mirror {mirror} missing cloned README"
        )

        # Mac mirror sees the sentinel too (proves the command ran under the
        # sync-completed workspace, not on an unsynced empty dir).
        assert (mirror / "startup-sentinel").exists()
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
