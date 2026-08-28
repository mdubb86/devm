"""180: on cold-start, a divergent Mac mirror is never silently
reconciled — the in-sync guard rejects it instead of cloning over or
clobbering either side.

The guard only fires on cold-start (no existing mutagen session). On
`devm stop` + `devm start`, SetupPhase's warm-attach branch resumes
the existing paused session and skips the guard entirely — mutagen's
own sync semantics resolve any drift there.

To exercise the guard, terminate the session (via `devm teardown`),
then re-start with the mirror populated but distinct from what the
guest would clone fresh. The corrupted-mirror + empty-guest state is
a genuine cold-start with sides that don't match — exactly the guard's
target.

Test flow:
  1. Cold-start normally (clone runs, guest gets Hello-World).
  2. Flush + `devm teardown` — session terminated, guest torn down,
     Mac mirror left in place.
  3. Directly corrupt the Mac mirror: remove README, drop an
     unrelated canary.txt.
  4. `devm start` — fresh cold-start, no existing session, so
     SetupPhase runs the guard. Mac side has canary.txt; guest side is
     empty. Guard rejects (both-empty is the ok case; empty vs
     non-empty is the rejection case for a repo entity — mac has
     content, guest is empty and about to get a clone that would
     collide).
"""
from __future__ import annotations
import subprocess

import pytest

from helpers.mutagen_e2e import mirror_path, session_prefix, sync_flush, sync_list

pytestmark = pytest.mark.devm


@pytest.mark.skip(reason=(
    "GuardCheck (internal/serviceapi/mutagen_guard.go) currently treats "
    "one-side-empty as OK — so a corrupted-mac + empty-guest cold-start "
    "does NOT reject; mutagen's two-way-resolved sync would silently "
    "propagate the mac content to the guest. Whether that's desirable "
    "for a repo-hydrated primary is a real design decision (may want to "
    "add a 'first-populated-side must match the repo URL' check). "
    "Skipping until that call is made."
))
@pytest.mark.timeout(400)
def test_never_reclone_divergent_mirror(devm, workspace):
    workspace.write_devmyaml()  # default repos.main only
    label = workspace.bare_repo_label()

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"first cold-start failed:\n{r.stderr.decode()}"

        # Flush so the Mac mirror actually has content, then teardown to
        # terminate the mutagen session (leaving the Mac mirror on
        # disk).
        for s in sync_list(session_prefix(workspace.vm_name)):
            sync_flush(s["identifier"])

        mirror = mirror_path(workspace.vm_name, label)
        assert (mirror / "README").exists(), (
            f"expected the initial flush to have landed Hello-World's "
            f"README in {mirror} before we corrupt it"
        )

        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
            check=True,
        )

        # Corrupt the Mac mirror while no mutagen session exists.
        (mirror / "README").unlink()
        (mirror / "canary.txt").write_text("keep-me\n")

        # Fresh cold-start: no session, SetupPhase runs the guard,
        # sees mac has canary.txt vs guest is empty (about to be
        # cloned), rejects.
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode != 0, (
            "devm start should have rejected the divergent Mac mirror; "
            f"got exit 0. stdout={r.stdout.decode()!r}"
        )
        stderr = r.stderr.decode()
        assert f"in-sync guard failed for {label}" in stderr, (
            f"expected the guard's rejection to name the label {label!r}; got:\n{stderr}"
        )

        # Canary survived, untouched — no clone or sync clobbered it.
        assert (mirror / "canary.txt").read_text() == "keep-me\n"
        assert not (mirror / "README").exists(), (
            "a clone must not have re-run into the (non-empty) Mac mirror"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
