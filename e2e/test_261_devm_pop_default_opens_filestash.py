"""261: devm pop <path> defaults to opening filestash URL, not a Mac app.
Uses the DEVM_POP_OPEN env-var seam (add if not present) that
substitutes for `open` in the popExecOpen call, so the URL is captured
in a temp file we can assert on."""
from __future__ import annotations
import os
import subprocess
import tempfile
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(240)
def test_devm_pop_default_opens_filestash(devm, workspace):
    workspace.write_devmyaml(no_repo=True)
    (workspace.path / "foo.txt").write_text("x\n")
    try:
        r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                           capture_output=True, timeout=180)
        assert r.returncode == 0

        # Substitute `open` via env-hook the CLI reads at runtime — a
        # tiny 'open' shim on PATH that just writes argv[1] to a file.
        with tempfile.TemporaryDirectory() as td:
            shim_dir = td
            captured = os.path.join(td, "captured-url")
            shim = os.path.join(shim_dir, "open")
            with open(shim, "w") as f:
                f.write(f'#!/bin/sh\nprintf "%s" "$1" > {captured}\n')
            os.chmod(shim, 0o755)
            env = os.environ.copy()
            env["PATH"] = shim_dir + ":" + env["PATH"]
            r = subprocess.run([devm.path, "pop", "foo.txt"],
                               cwd=str(workspace.path), env=env,
                               capture_output=True, timeout=30)
            assert r.returncode == 0, f"pop failed: {r.stderr.decode()!r}"
            with open(captured) as f:
                url = f.read()
            assert url.startswith(f"https://files.{workspace.vm_name}.e2e.test/files/local/"), (
                f"expected filestash URL; got {url!r}"
            )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                       capture_output=True, timeout=60)
