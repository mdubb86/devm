"""274: a guest-only git commit survives `devm teardown` + cold-start.

The Mac mirror of a `repos.main` entry is a mutagen-synced dir, and
mutagen's daemon-supplied default ignore list controls what gets
synced. If `.git/objects/pack/` is in that list, the mirror keeps
refs/HEAD but no objects after a guest-side `git gc` — on teardown the
guest-only commits die with the VM, since the mirror is the only copy
and it can no longer resolve HEAD. Pins the preservation contract so
the default ignore list can't silently drop packs again.

Fails pre-fix: the second cold-start's integrity gate catches the
broken mirror and refuses to proceed (`internal/serviceapi/mutagen_sessions.go`'s
verifyGitHEAD step), making `devm start` exit non-zero. Even if that
gate were removed, the commit SHA from the guest-only run would not be
reachable from the second guest.

Related:
  - test_140 covers scratch-volume persistence across teardown.
  - test_184 covers guest git identity provisioning.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.mutagen_e2e import mirror_path, session_prefix, sync_flush, sync_list

pytestmark = pytest.mark.devm

_TEST_NAME = "Devm E2E Bot"
_TEST_EMAIL = "devm-e2e-bot@example.com"


def _flush_all(vm_name: str) -> None:
    for s in sync_list(session_prefix(vm_name)):
        sync_flush(s["identifier"])


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_repo_mirror_preserves_git_history(devm, workspace):
    # Repo-local git identity on the workspace path — devm's
    # internal/provision.gitIdentity reads this and mirrors it into the
    # guest's ~/.gitconfig so a `git commit` in-guest succeeds without
    # a TTY prompt. Same pattern as test_184.
    subprocess.run(["git", "init", "-q", str(workspace.path)], check=True)
    subprocess.run(
        ["git", "-C", str(workspace.path), "config", "user.name", _TEST_NAME], check=True,
    )
    subprocess.run(
        ["git", "-C", str(workspace.path), "config", "user.email", _TEST_EMAIL], check=True,
    )

    workspace.write_devmyaml()
    primary_label = workspace.bare_repo_label()
    repo_guest = f"/home/devm/{primary_label}"

    try:
        # First cold-start: clones github.com/octocat/Hello-World into
        # the guest, mutagen-syncs it back to the Mac mirror.
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"first cold-start failed:\n{r.stderr.decode()}"

        # Make a guest-only commit AND pack it. The pack is the key —
        # loose objects aren't the ignore's target. `git gc` here packs
        # the new commit's loose object; after this, HEAD only resolves
        # if `.git/objects/pack/` is syncable.
        r = subprocess.run(
            [devm.path, "exec", "--", "bash", "-c",
             f"cd {repo_guest} && "
             "git commit --allow-empty -m 'e2e history' && "
             "git gc --quiet"],
            cwd=str(workspace.path), capture_output=True, timeout=120,
        )
        assert r.returncode == 0, (
            f"guest commit+gc failed:\n"
            f"stdout: {r.stdout.decode()!r}\nstderr: {r.stderr.decode()!r}"
        )

        # Capture the commit SHA so the second-cold-start assertion can
        # verify this specific commit (not just "some HEAD") survives.
        r = subprocess.run(
            [devm.path, "exec", "--", "git", "-C", repo_guest, "rev-parse", "HEAD"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, f"guest rev-parse HEAD failed:\n{r.stderr.decode()}"
        guest_sha = r.stdout.decode().strip()
        assert len(guest_sha) == 40, f"expected 40-char SHA, got {guest_sha!r}"

        # Flush before teardown — TeardownPhase terminates without an
        # explicit flush first, same as test_140.
        _flush_all(workspace.vm_name)

        # Mac mirror should now have the pack. This is the mechanism
        # check (pre-fix: no .pack files; post-fix: at least one).
        mirror_pack_dir = mirror_path(workspace.vm_name, primary_label) / ".git" / "objects" / "pack"
        packs = list(mirror_pack_dir.glob("*.pack")) if mirror_pack_dir.exists() else []
        assert packs, (
            f"Mac mirror has no .pack files after guest `git gc` — the default "
            f"ignore list is dropping them. Looked in {mirror_pack_dir}, "
            f"saw: {list(mirror_pack_dir.iterdir()) if mirror_pack_dir.exists() else 'dir does not exist'}"
        )

        # Mirror's HEAD must still resolve against local objects — if
        # the pack is missing, `rev-parse HEAD` on the mirror fails
        # with "bad object HEAD".
        r = subprocess.run(
            ["git", "-C", str(mirror_path(workspace.vm_name, primary_label)),
             "rev-parse", "--verify", "HEAD"],
            capture_output=True, timeout=30,
        )
        assert r.returncode == 0, (
            f"Mac mirror HEAD does not resolve against local objects — the "
            f"pack sync is still broken:\nstderr: {r.stderr.decode()!r}"
        )
        mirror_sha = r.stdout.decode().strip()
        assert mirror_sha == guest_sha, (
            f"Mac mirror HEAD mismatch: mirror {mirror_sha!r} vs guest {guest_sha!r}"
        )

        # Teardown: destroys VM disk. Mac mirror must survive with the
        # commit still reachable.
        r = subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"teardown failed:\n{r.stderr.decode()}"

        # Post-teardown: mirror's HEAD still resolves (packs survived
        # the VM's death because they were synced to the mirror pre-
        # teardown).
        r = subprocess.run(
            ["git", "-C", str(mirror_path(workspace.vm_name, primary_label)),
             "rev-parse", "--verify", "HEAD"],
            capture_output=True, timeout=30,
        )
        assert r.returncode == 0, (
            f"Mac mirror HEAD does not resolve post-teardown — pack sync "
            f"didn't preserve git history:\nstderr: {r.stderr.decode()!r}"
        )

        # Second cold-start: fresh VM re-hydrates from the mirror. The
        # integrity gate (verifyGitHEAD) must pass against the mirror's
        # content.
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=300,
        )
        assert r.returncode == 0, (
            f"second cold-start failed — mirror integrity check likely "
            f"rejected it:\n{r.stderr.decode()}"
        )

        _flush_all(workspace.vm_name)

        # The guest-only commit from the first VM is reachable in the
        # fresh VM's clone.
        r = subprocess.run(
            [devm.path, "exec", "--", "git", "-C", repo_guest,
             "cat-file", "-e", guest_sha],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, (
            f"commit {guest_sha!r} from first VM is not present in the "
            f"second VM's clone — pack sync lost it:\n"
            f"stderr: {r.stderr.decode()!r}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
