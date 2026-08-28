"""172: primary + secondary repos (`repos:` map) both hydrate on
cold-start, each into its own guest path and Mac mirror.

Declares a secondary entry in the `repos:` map with `volume: true`,
backed by a DISTINCT public github repo from the primary's, so a
passing test can't be explained by devm accidentally aliasing one
clone destination onto the other. Both `devm shell -- ls` reads must
see their own distinct content (Hello-World's README vs Spoon-Knife's
index.html).

The primary needs an explicit `primary: true` here (rather than
relying on the URL-nil heuristic) because, with two `repos:` entries
present, validateRepos requires exactly one primary marker when no
entry omits `url:`.
"""
from __future__ import annotations
import subprocess

import pytest

from helpers.mutagen_e2e import mirror_path, session_prefix, sync_flush, sync_list

pytestmark = pytest.mark.devm

# A second small public github repo, distinct from the fixture default.
# BareCloneName strips path + '.git' → 'Spoon-Knife'.
SECONDARY_URL = "https://github.com/octocat/Spoon-Knife.git"


@pytest.mark.timeout(300)
def test_multi_repo_both_mount(devm, workspace):
    primary_label = workspace.bare_repo_label()   # 'Hello-World'
    secondary_label = "Spoon-Knife"

    workspace.write_devmyaml(
        repos={
            "main": {
                "url": workspace.bare_repo_url(),
                "primary": True,
            },
            "secondary": {
                "url": SECONDARY_URL,
                "volume": True,
            },
        },
    )

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # Both repos land at /home/devm/<label> with their distinctive
        # top-level file names.
        r = subprocess.run(
            [devm.path, "shell", "--", "ls", f"/home/devm/{primary_label}"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert "README" in r.stdout.decode(), (
            f"primary Hello-World tree missing README; got:\n{r.stdout.decode()}"
        )

        r = subprocess.run(
            [devm.path, "shell", "--", "ls", f"/home/devm/{secondary_label}"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert "index.html" in r.stdout.decode(), (
            f"secondary Spoon-Knife tree missing index.html; got:\n{r.stdout.decode()}"
        )

        for s in sync_list(session_prefix(workspace.vm_name)):
            sync_flush(s["identifier"])

        assert (mirror_path(workspace.vm_name, primary_label) / "README").exists()
        assert (mirror_path(workspace.vm_name, secondary_label) / "index.html").exists()
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
