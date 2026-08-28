"""225: a `repos:` map with a URL-omitted primary and a URL-set
secondary (`volume: true`) hydrates both, each with its own Mac mirror.

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

# Second public github repo used as the secondary. Small, stable, and
# has a distinctive filename (`index.html`) we can pin the mirror
# against. Any other tiny public repo would do.
SECONDARY_URL = "https://github.com/octocat/Spoon-Knife.git"


@pytest.mark.timeout(300)
def test_repos_map_and_primary(devm, workspace):
    # Primary: workspace becomes a git repo whose `origin` points at
    # the fixture's public URL. `repos.main` omits `url:` so devm must
    # derive it from `git remote get-url origin`, and the label falls
    # back to the basename of the Mac cwd (the workspace dir).
    subprocess.run(["git", "init", "-q", str(workspace.path)], check=True)
    subprocess.run(
        ["git", "-C", str(workspace.path), "remote", "add", "origin", workspace.bare_repo_url()],
        check=True,
    )

    primary_label = workspace.path.name
    secondary_label = "spoon-knife"

    workspace.write_devmyaml(
        repos={
            "main": {},  # url derived from `origin`
            "secondary": {
                "url": SECONDARY_URL,
                "volume": True,       # opt into mirroring
                "label": secondary_label,
            },
        },
        # Extend network.allow beyond the default (github.com only) —
        # SECONDARY_URL also points at github.com, so the fixture
        # default covers it, but be explicit.
        network={"allow": ["github.com"]},
    )

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

        # Both repos land in the guest at /home/devm/<label>.
        r = subprocess.run(
            [devm.path, "shell", "--", "test", "-d", f"/home/devm/{primary_label}/.git"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, (
            f"primary label {primary_label!r} missing in guest:\n{r.stderr.decode()}"
        )
        r = subprocess.run(
            [devm.path, "shell", "--", "test", "-d", f"/home/devm/{secondary_label}/.git"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, (
            f"secondary label {secondary_label!r} missing in guest:\n{r.stderr.decode()}"
        )

        # Both labels produce their own mutagen session.
        sessions = sync_list(session_prefix(workspace.vm_name))
        by_name = {s["name"]: s for s in sessions}
        assert any(n.endswith(f"-{primary_label}") for n in by_name), (
            f"expected a session for primary label {primary_label!r}, got {list(by_name)}"
        )
        assert any(n.endswith(f"-{secondary_label}") for n in by_name), (
            f"expected a session for secondary label {secondary_label!r}, got {list(by_name)}"
        )

        # Force a flush and pin distinctive files on each Mac-side mirror.
        for s in sessions:
            sync_flush(s["identifier"])

        # Hello-World's `README` (no extension).
        primary_mirror = mirror_path(workspace.vm_name, primary_label)
        assert (primary_mirror / "README").exists(), (
            f"primary Mac mirror missing README at {primary_mirror}"
        )
        # Spoon-Knife ships an `index.html`.
        secondary_mirror = mirror_path(workspace.vm_name, secondary_label)
        assert (secondary_mirror / "index.html").exists(), (
            f"secondary Mac mirror missing index.html at {secondary_mirror}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
