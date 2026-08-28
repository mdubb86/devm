"""172: primary + secondary repos (`repos:` map) both hydrate on
cold-start, each into its own guest path and Mac mirror.

Declares a secondary entry in the `repos:` map with `volume: true`,
backed by a SECOND bare repo distinct from the primary's (so a
passing test can't be explained by devm accidentally aliasing one
clone destination onto the other). Both `devm shell -- cat` reads must
see their own distinct content.

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


def _make_bare_repo(tmp_path_factory, marker: str) -> str:
    """Standalone bare repo (independent of workspace.bare_repo_url()),
    with a distinguishing marker file so its clone is unambiguous."""
    work = tmp_path_factory.mktemp(f"{marker}-work")
    subprocess.run(["git", "-C", str(work), "init", "-q"], check=True)
    (work / f"{marker}.txt").write_text(f"{marker}\n")
    subprocess.run(["git", "-C", str(work), "add", "."], check=True)
    subprocess.run(
        ["git", "-C", str(work), "-c", "user.email=e2e@e2e", "-c", "user.name=e2e",
         "commit", "-q", "-m", "init"],
        check=True,
    )
    bare = tmp_path_factory.mktemp(f"{marker}-bare") / "repo.git"
    subprocess.run(["git", "clone", "--bare", "-q", str(work), str(bare)], check=True)
    return f"file://{bare}"


@pytest.mark.timeout(300)
def test_multi_repo_both_mount(devm, workspace, tmp_path_factory):
    secondary_url = _make_bare_repo(tmp_path_factory, "secondary")
    primary_label = workspace.bare_repo_label()
    secondary_label = "repo"  # BareCloneName of ".../repo.git"

    workspace.write_devmyaml(
        repos={
            "main": {"url": workspace.bare_repo_url(), "secret": "e2e_default", "primary": True},
            "secondary": {"url": secondary_url, "secret": "e2e_default", "volume": True},
        },
    )

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        r = subprocess.run(
            [devm.path, "shell", "--", "cat", f"/home/devm/{primary_label}/README.md"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert r.stdout.decode().strip() == "bare"

        r = subprocess.run(
            [devm.path, "shell", "--", "cat", f"/home/devm/{secondary_label}/secondary.txt"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert r.stdout.decode().strip() == "secondary"

        for s in sync_list(session_prefix(workspace.vm_name)):
            sync_flush(s["identifier"])

        assert (mirror_path(workspace.vm_name, primary_label) / "README.md").exists()
        assert (mirror_path(workspace.vm_name, secondary_label) / "secondary.txt").exists()
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
