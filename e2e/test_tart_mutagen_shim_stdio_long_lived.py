"""Long-lived sync stream test — write 60 files over 30s, verify all
arrive on the guest side. Confirms gRPC/vsock stream doesn't degrade.
"""
from __future__ import annotations
import subprocess
import time
import pytest
from helpers.mutagen_e2e import mirror_path, session_prefix, sync_flush, sync_list

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_shim_stdio_long_lived(devm, workspace):
    workspace.write_devmyaml()
    r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                       capture_output=True, timeout=180)
    assert r.returncode == 0, r.stderr.decode()
    label = workspace.bare_repo_label()
    mirror = mirror_path(workspace.vm_name, label)

    for i in range(60):
        (mirror / f"stream-{i:03d}.txt").write_text(str(i))
        time.sleep(0.5)

    sessions = sync_list(session_prefix(workspace.vm_name))
    sync_flush(sessions[0]["identifier"])

    # Verify all files landed in the guest.
    r = subprocess.run(
        [devm.path, "shell", "--", "ls", f"/home/devm/{label}"],
        cwd=str(workspace.path), capture_output=True, timeout=60,
    )
    assert r.returncode == 0
    files = r.stdout.decode().split()
    for i in range(60):
        assert f"stream-{i:03d}.txt" in files, \
            f"missing stream-{i:03d}.txt; ls output: {files}"
