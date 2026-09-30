"""262: devm pop --native <in-mirror-path> opens the Mac-side mirror
file directly — a Mac path, not a filestash URL. Uses the same
DEVM_POP_OPEN-style `open` shim as 261 to capture the argument `open`
receives, then asserts it's the Mac mirror path (internal/serviceapi's
<RuntimeDir>/<project>/<label>/ layout), not an https:// URL.

The default `repos.main` fixture clones octocat/Hello-World, which
ships a top-level `README` — see test_170_repo_workspace_cold_start.py
for the same fixture/flush pattern this test reuses."""
from __future__ import annotations
import os
import subprocess
import tempfile
import pytest

from helpers.mutagen_e2e import mirror_path, session_prefix, sync_flush, sync_list

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_devm_pop_native_in_mirror(devm, workspace):
    label = workspace.bare_repo_label()
    workspace.write_devmyaml()
    try:
        r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                           capture_output=True, timeout=180)
        assert r.returncode == 0, f"cold-start failed: {r.stderr.decode()!r}"

        # Force the Mac-side mirror to catch up with the guest clone
        # before popping — mutagen sync is asynchronous, so right after
        # `devm start` the mirror may still be empty (see test_170).
        sessions = sync_list(session_prefix(workspace.vm_name))
        assert len(sessions) == 1, f"expected exactly one session, got {sessions}"
        r = sync_flush(sessions[0]["identifier"])
        assert r.returncode == 0, f"mutagen sync flush failed:\n{r.stderr}"

        mirror = mirror_path(workspace.vm_name, label)
        assert (mirror / "README").exists(), (
            f"primary Mac mirror {mirror} missing cloned README"
        )

        with tempfile.TemporaryDirectory() as td:
            captured = os.path.join(td, "captured-arg")
            shim = os.path.join(td, "open")
            with open(shim, "w") as f:
                f.write(f'#!/bin/sh\nprintf "%s" "$1" > {captured}\n')
            os.chmod(shim, 0o755)
            env = os.environ.copy()
            env["PATH"] = td + ":" + env["PATH"]
            r = subprocess.run([devm.path, "pop", "--native", "README"],
                               cwd=str(workspace.path), env=env,
                               capture_output=True, timeout=30)
            assert r.returncode == 0, f"pop --native failed: {r.stderr.decode()!r}"
            with open(captured) as f:
                mac_path = f.read()
            assert not mac_path.startswith("http"), f"expected mac path; got {mac_path!r}"
            assert mac_path == str(mirror / "README"), (
                f"expected mirror README path {mirror / 'README'}; got {mac_path!r}"
            )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                       capture_output=True, timeout=60)
