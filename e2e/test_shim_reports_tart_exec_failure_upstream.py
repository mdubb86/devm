"""Shim propagates tart exec failure exit code so mutagen surfaces a real
error — no silent hang.
"""
from __future__ import annotations
import os
import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_shim_propagates_tart_failure(devm, workspace, tmp_path):
    # Preempt tart in PATH with a stub that exits non-zero. This will
    # cause the shim to fail; mutagen should see a real error.
    stub_dir = tmp_path / "bin"
    stub_dir.mkdir()
    stub = stub_dir / "tart"
    stub.write_text("#!/bin/bash\nexit 99\n")
    stub.chmod(0o755)

    # Runtime dir under devm-e2e state (test infra uses e2e identity).
    # The shim is extracted at daemon startup regardless of any
    # project, so it's already present here.
    home = os.path.expanduser("~/Library/Application Support/devm-e2e")
    shim = os.path.join(home, "mutagen-ssh-dir", "ssh")
    assert os.path.exists(shim), f"shim ssh missing at {shim}"

    env = os.environ.copy()
    env["PATH"] = str(stub_dir) + ":" + env["PATH"]

    r = subprocess.run(
        [shim, "-oConnectTimeout=5", "devm@devm-fake", "true"],
        env=env, capture_output=True, timeout=10,
    )
    assert r.returncode == 99, \
        f"shim must propagate tart's exit code 99, got {r.returncode}"
