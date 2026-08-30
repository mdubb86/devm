"""Pin: a corrupt mac-mirror repo state fails `devm start` loud during
volume-sync, naming the mirror path and suggesting `devm volume rm`.

Regression fence for the Shelfmates-observed pattern (truncated .git with
`fatal: bad object HEAD` silently adopted into the guest via mutagen).
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.mutagen_e2e import mirror_path

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_corrupt_mac_mirror_fails_loud(devm, workspace):
    workspace.write_devmyaml(
        repos={
            "main": {
                "url": "https://github.com/octocat/Hello-World.git",
                "primary": True,
            },
        },
        packages=["git"],
        network={"allow": ["github.com", "deb.debian.org", "security.debian.org"]},
    )

    # Pre-seed the mac mirror with a broken .git directory before the FIRST
    # `devm start` — no mutagen session exists yet, so this is just a file
    # write to the destination directory.
    label = workspace.bare_repo_label()  # convention: repo label under mutagen-volumes
    mirror = mirror_path(workspace.vm_name, label)
    mirror.mkdir(parents=True, exist_ok=True)
    git_dir = mirror / ".git"
    git_dir.mkdir(parents=True, exist_ok=True)
    (git_dir / "HEAD").write_text("ref: refs/heads/main\n")
    # Seed minimal .git structure to trigger git's "truncated repo" error.
    # Need .git/objects and .git/refs/heads/ for git rev-parse to recognize
    # it as a repo and fail on the missing HEAD object.
    (git_dir / "objects").mkdir(parents=True, exist_ok=True)
    (git_dir / "refs").mkdir(parents=True, exist_ok=True)
    (git_dir / "refs" / "heads").mkdir(parents=True, exist_ok=True)
    # No refs/heads/main object → git rev-parse --verify HEAD fails.

    try:
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=120,
        )
        assert r.returncode != 0, (
            f"devm start should have failed on corrupt mac mirror; "
            f"got rc=0\nstdout:\n{r.stdout.decode()}\nstderr:\n{r.stderr.decode()}"
        )
        stderr = r.stderr.decode()
        # Load-bearing assertions: error must name the mirror path AND
        # mention the integrity check, so a user can diagnose.
        assert str(mirror) in stderr, (
            f"error must name mirror path {mirror}; got:\n{stderr}"
        )
        assert "integrity check" in stderr, (
            f"error must mention 'integrity check'; got:\n{stderr}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
