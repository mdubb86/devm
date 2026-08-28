"""174: a plain scratch `volumes:` entry coexists with a repo-hydrated
`repos:` secondary (`volume: true`).

A repo secondary that wants its own Mac mirror lives in the `repos:`
map with `volume: true` (schema.Volume has no Repo field). This pins
that both coexist correctly in one project: the scratch volume stays
empty in the guest on cold-start and persists a guest-written sentinel
across a teardown + restart cycle (via its Mac mirror, not VM disk
persistence), while the repo secondary is cloned in the guest and its
own mirror holds the clone content.
"""
from __future__ import annotations
import subprocess

import pytest

from helpers.mutagen_e2e import mirror_path, session_prefix, sync_flush, sync_list

pytestmark = pytest.mark.devm

# Public github secondary — distinct from the fixture's Hello-World
# so we can pin distinctive top-level files.
SECONDARY_URL = "https://github.com/octocat/Spoon-Knife.git"


def _flush_all(vm_name: str) -> None:
    for s in sync_list(session_prefix(vm_name)):
        sync_flush(s["identifier"])


@pytest.mark.timeout(400)
def test_scratch_and_repo_volumes_coexist(devm, workspace):
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
        volumes={"scratch": "/var/lib/scratch"},
    )

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=300,
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

        # Repo secondary: hydrated in the guest.
        r = subprocess.run(
            [devm.path, "shell", "--", "ls", f"/home/devm/{secondary_label}"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert "index.html" in r.stdout.decode(), (
            f"secondary Spoon-Knife tree missing index.html; got:\n{r.stdout.decode()}"
        )

        # Write a sentinel into scratch, flush all sessions to their
        # Mac mirrors, then teardown + restart.
        r = subprocess.run(
            [devm.path, "shell", "--", "sudo", "sh", "-c",
             "echo hi > /var/lib/scratch/sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()

        _flush_all(workspace.vm_name)
        assert (mirror_path(workspace.vm_name, "scratch") / "sentinel").exists()
        assert (mirror_path(workspace.vm_name, secondary_label) / "index.html").exists()

        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )

        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"restart cold-start failed:\n{r.stderr.decode()}"

        _flush_all(workspace.vm_name)

        # Scratch persisted the sentinel and nothing else appeared.
        r = subprocess.run(
            [devm.path, "shell", "--", "ls", "/var/lib/scratch"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert r.stdout.decode().strip() == "sentinel"

        # Repo secondary's clone survived too (re-synced from its Mac
        # mirror, never re-cloned or wiped).
        r = subprocess.run(
            [devm.path, "shell", "--", "ls", f"/home/devm/{secondary_label}"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert "index.html" in r.stdout.decode()
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
