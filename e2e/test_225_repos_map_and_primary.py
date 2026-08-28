"""225: a `repos:` map with a URL-omitted primary and a URL-set
secondary (`volume: true`) hydrates both, each with its own Mac
mirror.

findPrimaryRepoName (internal/serviceapi/mutagen_sessions.go) resolves
the sole URL-nil entry as primary when no entry is explicitly marked
`primary: true`. The secondary opts into mirroring via `volume: true`
-- without it, BuildEntities excludes a secondary entirely (secondary
repos default to unmirrored).
"""
from __future__ import annotations
import subprocess

import pytest

from helpers.mutagen_e2e import mirror_path, session_prefix, sync_flush, sync_list

pytestmark = pytest.mark.devm


def _make_bare_repo(tmp_path_factory, marker: str) -> str:
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
def test_repos_map_and_primary(devm, workspace, tmp_path_factory):
    subprocess.run(["git", "init", "-q", str(workspace.path)], check=True)
    subprocess.run(
        ["git", "-C", str(workspace.path), "remote", "add", "origin", workspace.bare_repo_url()],
        check=True,
    )
    secondary_url = _make_bare_repo(tmp_path_factory, "secondary")

    primary_label = workspace.path.name
    secondary_label = "secondary"  # BareCloneName("file://.../repo.git") -> "repo"; use explicit label instead

    workspace.write_devmyaml(
        repos={
            "main": {"secret": "e2e_default"},
            "secondary": {
                "url": secondary_url, "secret": "e2e_default",
                "volume": True, "label": secondary_label,
            },
        },
    )

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

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

        sessions = sync_list(session_prefix(workspace.vm_name))
        by_name = {s["name"]: s for s in sessions}
        assert any(n.endswith(f"-{primary_label}") for n in by_name), (
            f"expected a session for primary label {primary_label!r}, got {list(by_name)}"
        )
        assert any(n.endswith(f"-{secondary_label}") for n in by_name), (
            f"expected a session for secondary label {secondary_label!r}, got {list(by_name)}"
        )

        for s in sessions:
            sync_flush(s["identifier"])

        assert (mirror_path(workspace.vm_name, primary_label) / "README.md").exists(), (
            f"primary Mac mirror missing README.md at "
            f"{mirror_path(workspace.vm_name, primary_label)}"
        )
        assert (mirror_path(workspace.vm_name, secondary_label) / "secondary.txt").exists(), (
            f"secondary Mac mirror missing secondary.txt at "
            f"{mirror_path(workspace.vm_name, secondary_label)}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
