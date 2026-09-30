"""263: devm pop --native <out-of-mirror-path> cp's from guest to Mac
scratch and opens the scratch file. Assert scratch file contents match
the guest source (via a pre-seeded /tmp/e2e-marker file on the guest)."""
from __future__ import annotations
import os
import subprocess
import tempfile
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(240)
def test_devm_pop_native_out_of_mirror_cp(devm, workspace):
    workspace.write_devmyaml(no_repo=True)
    try:
        subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       check=True, capture_output=True, timeout=180)

        # Seed a guest-only file.
        r = subprocess.run(
            [devm.path, "exec", "bash", "-c", "echo 'e2e cp bytes' > /tmp/e2e-cp-marker"],
            cwd=str(workspace.path), capture_output=True, timeout=15)
        assert r.returncode == 0

        with tempfile.TemporaryDirectory() as td:
            captured = os.path.join(td, "captured-arg")
            shim = os.path.join(td, "open")
            with open(shim, "w") as f:
                f.write(f'#!/bin/sh\nprintf "%s" "$1" > {captured}\n')
            os.chmod(shim, 0o755)
            env = os.environ.copy()
            env["PATH"] = td + ":" + env["PATH"]
            r = subprocess.run([devm.path, "pop", "--native", "/tmp/e2e-cp-marker"],
                               cwd=str(workspace.path), env=env,
                               capture_output=True, timeout=60)
            assert r.returncode == 0, f"pop --native failed: {r.stderr.decode()!r}"
            with open(captured) as f:
                scratch_path = f.read()
            assert not scratch_path.startswith("http"), f"expected mac path; got {scratch_path!r}"
            with open(scratch_path) as f:
                body = f.read()
            assert body == "e2e cp bytes\n", (
                f"scratch file bytes don't match guest source: got {body!r}")
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                       capture_output=True, timeout=60)
