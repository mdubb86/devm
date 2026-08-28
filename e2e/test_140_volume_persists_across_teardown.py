"""140: a volume's contents survive `devm teardown` + cold-start.

A `volumes:` entry (and the primary repo) is a Mac-side mirror dir
kept in sync with a guest path via mutagen, not a live bind mount.
Persistence means: content written in the guest, once flushed to the
Mac mirror at
`~/Library/Application Support/devm-e2e/<project>/<label>/`, survives
the VM disk being destroyed at teardown, and a fresh guest re-syncs it
back in on the next cold-start.

If this fails: SetupPhase isn't creating the mirror dir at the
expected path, OR TeardownPhase is wiping mirrors along with the VM,
OR the guard is rejecting the second cold-start's session recreate
against the (correctly) non-empty Mac mirror.

Also covers the auto-managed PRIMARY repo mirror (the fixture's
default `repos.main` entry) -- same persistence contract applies to
its guest clone dir as to any named `volumes:` entry.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.mutagen_e2e import mirror_path, session_prefix, sync_flush, sync_list

pytestmark = pytest.mark.devm


def _flush_all(vm_name: str) -> None:
    for s in sync_list(session_prefix(vm_name)):
        sync_flush(s["identifier"])


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_volume_persists_across_teardown(devm, workspace, sandbox_name):
    workspace.write_devmyaml(volumes={"scratch": "/var/lib/scratch"})
    primary_label = f"{workspace.path.name}-repo"  # BareCloneName of bare_repo_url()

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"first cold-start failed:\n{r.stderr.decode()}"

        # Sentinel into the named scratch volume (root-owned system path).
        r = subprocess.run(
            [devm.path, "shell", "--", "sudo", "sh", "-c",
             "echo hello > /var/lib/scratch/sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"first cold-start write failed:\n{r.stderr.decode()}"

        # Sentinel into the auto-managed primary repo's guest clone.
        r = subprocess.run(
            [devm.path, "shell", "--", "sh", "-c",
             f"echo hello > /home/devm/{primary_label}/primary-sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"primary sentinel write failed:\n{r.stderr.decode()}"

        # Force both sessions to converge to the Mac mirror before
        # teardown -- TeardownPhase terminates sessions without an
        # explicit flush first.
        sessions = sync_list(session_prefix(workspace.vm_name))
        assert len(sessions) == 2, f"expected sessions for scratch + primary, got {sessions}"
        for s in sessions:
            r = sync_flush(s["identifier"])
            assert r.returncode == 0, f"sync flush {s['name']} failed:\n{r.stderr}"

        assert (mirror_path(workspace.vm_name, "scratch") / "sentinel").exists(), (
            f"scratch Mac mirror missing sentinel at "
            f"{mirror_path(workspace.vm_name, 'scratch')}"
        )
        assert (mirror_path(workspace.vm_name, primary_label) / "primary-sentinel").exists(), (
            f"primary Mac mirror missing primary-sentinel at "
            f"{mirror_path(workspace.vm_name, primary_label)}"
        )

        # Teardown: destroys VM disk. Mac mirrors must survive.
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )

        assert (mirror_path(workspace.vm_name, "scratch") / "sentinel").exists(), (
            "scratch Mac mirror was wiped by teardown -- it must survive "
            "independently of the destroyed VM disk"
        )
        assert (mirror_path(workspace.vm_name, primary_label) / "primary-sentinel").exists(), (
            "primary Mac mirror was wiped by teardown"
        )

        # Second cold-start: a fresh, empty guest re-syncs from the
        # (correctly non-empty) Mac mirror -- the guard must accept
        # this (guest.Count == 0 side of the fast path).
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"second cold-start failed:\n{r.stderr.decode()}"

        _flush_all(workspace.vm_name)

        r = subprocess.run(
            [devm.path, "shell", "--", "cat", "/var/lib/scratch/sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, (
            f"post-teardown scratch read failed (rc={r.returncode}):\n"
            f"stderr: {r.stderr.decode()!r}"
        )
        assert r.stdout.decode().strip() == "hello", (
            f"sentinel content mismatch: got {r.stdout.decode()!r}, want 'hello\\n'"
        )

        r = subprocess.run(
            [devm.path, "shell", "--", "cat", f"/home/devm/{primary_label}/primary-sentinel"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, (
            f"post-teardown primary read failed (rc={r.returncode}):\n"
            f"stderr: {r.stderr.decode()!r}"
        )
        assert r.stdout.decode().strip() == "hello", (
            f"primary sentinel content mismatch: got {r.stdout.decode()!r}, want 'hello\\n'"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
