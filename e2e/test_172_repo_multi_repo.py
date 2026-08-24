"""172: primary + secondary repo volumes both hydrate on cold-start.

Declares a secondary volume under `volumes:` with its own `repo:`
block, backed by a SECOND bare repo distinct from the primary's (so a
passing test can't be explained by devm accidentally aliasing one
clone destination onto the other). Both mounts must appear via
`devm shell -- ls`.
"""
from __future__ import annotations
import subprocess

import pytest

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

    workspace.write_devmyaml(
        volumes={
            "secondary": {
                "path": "/mnt/secondary",
                "repo": {"url": secondary_url, "secret": "e2e_default"},
            },
        },
    )

    try:
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # Primary mount ($WORKSPACE) has the fixture's default clone.
        r = subprocess.run(
            [devm.path, "shell", "--", "sh", "-c", 'ls "$WORKSPACE"'],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert "README.md" in r.stdout.decode()

        # Secondary mount has its own distinct clone.
        r = subprocess.run(
            [devm.path, "shell", "--", "ls", "/mnt/secondary"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert "secondary.txt" in r.stdout.decode()

        # Mac-side storage for both.
        assert (workspace.volume_path() / "README.md").exists()
        assert (workspace.volume_path("secondary") / "secondary.txt").exists()
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
