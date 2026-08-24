"""174: mixed `volumes:` — one repo-hydrated, one plain scratch volume.

`volumes:` accepts either shape per entry: a bare guest-path string
(scratch, no hydration) or a `{path, repo}` mapping (hydrated from
git). This pins that both shapes coexist correctly in one project:
the scratch volume stays empty on cold-start and persists writes
across a teardown + restart cycle, while the repo volume is cloned.
"""
from __future__ import annotations
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(400)
def test_scratch_and_repo_volumes_coexist(devm, workspace):
    url = workspace.bare_repo_url()
    workspace.write_devmyaml(
        volumes={
            "scratch": "/var/lib/scratch",
            "repohydrated": {
                "path": "/mnt/repohydrated",
                "repo": {"url": url, "secret": "e2e_default"},
            },
        },
    )

    try:
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # Scratch volume: empty on cold-start (never hydrated).
        r = subprocess.run(
            [devm.path, "shell", "--", "ls", "/var/lib/scratch"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert r.stdout.decode().strip() == "", (
            f"scratch volume should be empty, got: {r.stdout.decode()!r}"
        )

        # Repo volume: hydrated with clone content.
        r = subprocess.run(
            [devm.path, "shell", "--", "ls", "/mnt/repohydrated"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert "README.md" in r.stdout.decode()

        # Write a sentinel into scratch, then teardown + restart.
        r = subprocess.run(
            [devm.path, "shell", "--", "sudo", "sh", "-c",
             "echo hi > /var/lib/scratch/sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()

        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )

        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"restart cold-start failed:\n{r.stderr.decode()}"

        # Scratch persisted the sentinel and nothing else appeared.
        r = subprocess.run(
            [devm.path, "shell", "--", "ls", "/var/lib/scratch"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert r.stdout.decode().strip() == "sentinel"

        # Repo volume's clone survived too (never re-cloned or wiped).
        r = subprocess.run(
            [devm.path, "shell", "--", "ls", "/mnt/repohydrated"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert "README.md" in r.stdout.decode()
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
