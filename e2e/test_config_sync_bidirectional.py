"""devm.yaml crosses the Mac <-> guest boundary through its own
dedicated mutagen session (internal/serviceapi/config_sync.go's
SetupConfigSync), independent of the workspace/repo sync. Proves both
directions: a Mac-side edit lands at /home/devm/devm.yaml in the
guest, and a guest-side edit lands back at the daemon's state-dir
copy on the Mac.
"""
from __future__ import annotations

import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(300)
def test_config_flows_both_ways(workspace, devm):
    workspace.write_devmyaml(no_repo=True, env={"MAC_WRITE": "0"})
    try:
        cold = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=180,
        )
        assert cold.returncode == 0, f"start failed: {cold.stderr.decode()!r}"

        # Mac -> guest.
        workspace.patch_devmyaml(env={"MAC_WRITE": "1"})
        deadline = time.monotonic() + 30
        guest_content = ""
        while time.monotonic() < deadline:
            cat = subprocess.run(
                [devm.path, "exec", "cat", "/home/devm/devm.yaml"],
                cwd=str(workspace.path), capture_output=True, timeout=15,
            )
            guest_content = cat.stdout.decode()
            if "MAC_WRITE" in guest_content:
                break
            time.sleep(1)
        assert "MAC_WRITE" in guest_content, (
            f"guest never saw the Mac-side edit:\n{guest_content}"
        )

        # Guest -> Mac. Appended under the existing `env:` block (last
        # top-level key written above) so the result stays valid,
        # strictly-decodable YAML -- an unindented top-level key would
        # fail devm's KnownFields(true) decode on the next config.Load
        # (e.g. this fixture's own `devm teardown`).
        append = subprocess.run(
            [devm.path, "exec", "bash", "-c",
             "echo '  GUEST_WRITE: \"1\"' >> /home/devm/devm.yaml"],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert append.returncode == 0, append.stderr.decode()

        deadline = time.monotonic() + 30
        mac_content = ""
        while time.monotonic() < deadline:
            mac_content = workspace.devmyaml_path.read_text()
            if "GUEST_WRITE" in mac_content:
                break
            time.sleep(1)
        assert "GUEST_WRITE" in mac_content, (
            f"Mac side never saw the guest-side edit:\n{mac_content}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
