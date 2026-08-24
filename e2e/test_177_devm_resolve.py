"""177: `devm resolve` translates VM-emitted paths to Mac-side storage.

The primary volume is bind-mounted at the guest path that literally
equals the Mac cwd string (the $WORKSPACE convention), so a file
written directly into the volume's Mac-side storage is reachable at
the same absolute path a guest process would have printed.
"""
from __future__ import annotations
import subprocess
import tempfile

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_resolve_abs_relative_outside_and_open(devm, workspace):
    workspace.write_devmyaml()

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, r.stderr.decode()

        (workspace.volume_path() / "note.txt").write_text("hello\n")
        expected = str(workspace.volume_path() / "note.txt")

        # Absolute path inside the workspace.
        guest_abs = str(workspace.path / "note.txt")
        r = subprocess.run(
            [devm.path, "resolve", guest_abs],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert r.stdout.decode().strip() == expected

        # Relative path, resolved against cwd inside the project.
        r = subprocess.run(
            [devm.path, "resolve", "note.txt"],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert r.stdout.decode().strip() == expected

        # Absolute path outside any known workspace -> clear error.
        with tempfile.TemporaryDirectory() as outside:
            r = subprocess.run(
                [devm.path, "resolve", f"{outside}/whatever.png"],
                cwd=str(workspace.path), capture_output=True, timeout=15,
            )
            assert r.returncode != 0
            assert "not inside any known devm workspace" in r.stderr.decode()

        # --open on a text file: exits 0 (skip verifying the actual UI).
        r = subprocess.run(
            [devm.path, "resolve", "--open", guest_abs],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert r.returncode == 0, r.stderr.decode()
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
