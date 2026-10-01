"""260: after a cold start, the bundled filestash service is up in the
guest and responds on 8941. Guest-internal curl only — Mac-side
routing arrives in Task 4."""
from __future__ import annotations
import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_filestash_bundle_installed_and_responds(devm, workspace):
    workspace.write_devmyaml(no_repo=True)
    try:
        r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                           capture_output=True, timeout=180)
        assert r.returncode == 0, f"cold-start failed: {r.stderr.decode()!r}"

        # systemd unit is active
        r = subprocess.run(
            [devm.path, "exec", "systemctl", "is-active", "filestash.service"],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert r.returncode == 0 and r.stdout.decode().strip() == "active", (
            f"filestash.service not active: {r.stdout!r} {r.stderr!r}"
        )

        # In-guest curl to the filestash port succeeds
        r = subprocess.run(
            [devm.path, "exec", "curl", "-sf", "-m", "5", "-o", "/dev/null", "http://127.0.0.1:8941/"],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert r.returncode == 0, (
            f"in-guest curl to filestash 127.0.0.1:8941 failed: "
            f"{r.stdout!r} {r.stderr!r}"
        )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                       capture_output=True, timeout=60)
