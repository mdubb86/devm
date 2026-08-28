"""174: a plain scratch `volumes:` entry coexists with a repo-hydrated
`repos:` secondary (`volume: true`).

Under the mutagen-volumes model, a repo secondary that wants its own
Mac mirror lives in the `repos:` map with `volume: true` -- the old
`volumes.<name>.repo:` embedded shape is gone (schema.Volume has no
Repo field). This pins that both coexist correctly in one project: the
scratch volume stays empty in the guest on cold-start and persists a
guest-written sentinel across a teardown + restart cycle (via its Mac
mirror, not VM disk persistence), while the repo secondary is cloned
in the guest and its own mirror holds the clone content.
"""
from __future__ import annotations
import subprocess

import pytest

from helpers.mutagen_e2e import mirror_path, session_prefix, sync_flush, sync_list

pytestmark = pytest.mark.devm


def _flush_all(vm_name: str) -> None:
    for s in sync_list(session_prefix(vm_name)):
        sync_flush(s["identifier"])


@pytest.mark.timeout(400)
def test_scratch_and_repo_volumes_coexist(devm, workspace, tmp_path_factory):
    work = tmp_path_factory.mktemp("repohydrated-work")
    subprocess.run(["git", "-C", str(work), "init", "-q"], check=True)
    (work / "repohydrated.txt").write_text("repohydrated\n")
    subprocess.run(["git", "-C", str(work), "add", "."], check=True)
    subprocess.run(
        ["git", "-C", str(work), "-c", "user.email=e2e@e2e", "-c", "user.name=e2e",
         "commit", "-q", "-m", "init"],
        check=True,
    )
    bare = tmp_path_factory.mktemp("repohydrated-bare") / "repohydrated.git"
    subprocess.run(["git", "clone", "--bare", "-q", str(work), str(bare)], check=True)
    repohydrated_url = f"file://{bare}"
    repohydrated_label = "repohydrated"  # BareCloneName of ".../repohydrated.git"

    workspace.write_devmyaml(
        repos={
            "main": {"url": workspace.bare_repo_url(), "secret": "e2e_default", "primary": True},
            "repohydrated": {"url": repohydrated_url, "secret": "e2e_default", "volume": True},
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

        # Repo secondary: hydrated with clone content in the guest.
        r = subprocess.run(
            [devm.path, "shell", "--", "cat", f"/home/devm/{repohydrated_label}/repohydrated.txt"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert r.stdout.decode().strip() == "repohydrated"

        # Write a sentinel into scratch, flush both sessions to their
        # Mac mirrors, then teardown + restart.
        r = subprocess.run(
            [devm.path, "shell", "--", "sudo", "sh", "-c",
             "echo hi > /var/lib/scratch/sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()

        _flush_all(workspace.vm_name)
        assert (mirror_path(workspace.vm_name, "scratch") / "sentinel").exists()
        assert (mirror_path(workspace.vm_name, repohydrated_label) / "repohydrated.txt").exists()

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

        # Repo secondary's clone survived too (re-synced from its
        # Mac mirror, never re-cloned or wiped).
        r = subprocess.run(
            [devm.path, "shell", "--", "cat", f"/home/devm/{repohydrated_label}/repohydrated.txt"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert r.stdout.decode().strip() == "repohydrated"
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
