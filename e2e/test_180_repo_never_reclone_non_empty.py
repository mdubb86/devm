"""180: a divergent Mac mirror is never silently reconciled -- the
in-sync guard rejects it instead of cloning over or clobbering either
side.

A truly fresh cold-start can't exercise a REJECTION directly: the
guard's fast path treats an empty side as never conflicting
(GuardCheck, internal/serviceapi/mutagen_guard.go), and a brand-new
guest is always empty before its own clone runs -- so the first
`devm start` on a project always passes trivially. To reach a genuine
rejection, this test:

  1. Cold-starts normally (clone runs, guest gets README.md).
  2. `devm stop` -- StopPhase flushes the session before pausing it, so
     the Mac mirror ends up holding the same README.md (VM disk is
     merely powered off, not destroyed; the guest's README.md is still
     there too).
  3. Directly corrupts the Mac mirror while stopped: removes README.md,
     writes an unrelated canary.txt -- simulating "the Mac-side storage
     already has different content" without ever letting mutagen touch
     it.
  4. `devm start` again -- SetupPhase re-scans both sides (mirror:
     canary.txt only; guest: README.md only, untouched since stop
     doesn't wipe the VM disk) and must reject: divergent non-empty
     sides, naming the label.

Pins: the guard fires before any clone attempt (neither side was ever
empty by the time the guard runs, so `macSide.Count==0 &&
guestSide.Count==0` never holds), and the Mac-side canary survives
untouched by the rejected attempt.
"""
from __future__ import annotations
import subprocess

import pytest

from helpers.mutagen_e2e import mirror_path, session_prefix, sync_flush, sync_list

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_never_reclone_divergent_mirror(devm, workspace):
    workspace.write_devmyaml()  # default repos.main only
    label = f"{workspace.path.name}-repo"

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"first cold-start failed:\n{r.stderr.decode()}"

        r = subprocess.run(
            [devm.path, "stop", "--yes"], cwd=str(workspace.path),
            capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"devm stop failed:\n{r.stderr.decode()}"

        mirror = mirror_path(workspace.vm_name, label)
        assert (mirror / "README.md").exists(), (
            f"expected StopPhase's flush to have landed README.md in the "
            f"Mac mirror at {mirror} before this test corrupts it"
        )
        (mirror / "README.md").unlink()
        (mirror / "canary.txt").write_text("keep-me\n")

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

        # Canary survived, untouched -- no clone or sync clobbered it.
        assert (mirror / "canary.txt").read_text() == "keep-me\n"
        assert not (mirror / "README.md").exists(), (
            "a clone must not have re-run into the (non-empty) Mac mirror"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
