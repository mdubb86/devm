"""210: cold-start hydration clones the primary repo IN THE GUEST.

The mutagen-volumes model hydrates repos entirely inside the guest:
`CloneRepoInGuest` (internal/serviceapi/mutagen_cold_start.go) runs
`git clone <URL> /home/devm/<label>` as the guest's devm user during
the begin-provisioning window — softnet is FORWARDING (iron-proxy in
the path) with authority in passthrough mode; the clone reaches origin
through iron-proxy, with the secrets transform available for private
repos though not needed for Hello-World.

Pins:
  - `.git` exists in the guest at `/home/devm/<label>/` after
    `devm start` on a fresh workspace with the default `repos.main`
    entry — that is, cold-start actually ran a guest clone.
  - The cloned tree carries content the fixture's public URL is known
    to serve (Hello-World's `README` file), catching a regression where
    the clone claimed success but ended up empty (an empty guest dir
    would still contain the target dir itself, so `.git` is the tight
    check; README is the belt).
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.mutagen_e2e import mirror_path

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_cold_start_guest_clone(devm, workspace):
    workspace.write_devmyaml()  # default repos.main -> Hello-World
    label = workspace.bare_repo_label()

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

        guest_dir = f"/home/devm/{label}"

        r = subprocess.run(
            [devm.path, "shell", "--", "test", "-d", f"{guest_dir}/.git"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, (
            f"{guest_dir}/.git missing in guest — clone did not land at the "
            f"expected guest path:\n{r.stderr.decode()}"
        )

        r = subprocess.run(
            [devm.path, "shell", "--", "ls", guest_dir],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert "README" in r.stdout.decode(), (
            f"expected README in cloned tree; got:\n{r.stdout.decode()}"
        )

        # The Mac-side mirror is already populated by the time `devm start`
        # returns -- WaitForInitialSync blocks start on the initial sync
        # completing, so no manual `sync flush` is needed here.
        mirror = mirror_path(workspace.vm_name, label)
        assert (mirror / "README").exists(), (
            f"Mac mirror {mirror} missing cloned README right after "
            f"`devm start` returned -- WaitForInitialSync should have "
            f"already brought it in sync"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
