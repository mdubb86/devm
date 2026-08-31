"""145: `devm volume ls` lists this project's declared volumes.

Small end-to-end test: declare two volumes, cold-start (populates
one with content, leaves one empty), run `devm volume ls` and check
the output includes both names, paths, and non-zero size on the
populated one. Also asserts the auto-managed PRIMARY workspace volume
(synthesized from the fixture's default `repo:` block) is listed
alongside the declared ones.
"""
from __future__ import annotations

import re
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_volume_ls(devm, workspace, sandbox_name):
    workspace.write_devmyaml(
        install=[
            "sudo mkdir -p /var/lib/data /var/lib/cache && sudo sh -c 'echo x > /var/lib/data/sentinel'",
        ],
        volumes={
            "data":  "/var/lib/data",
            "cache": "/var/lib/cache",
        },
    )
    try:
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        r = subprocess.run(
            [devm.path, "volume", "ls"],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert r.returncode == 0, f"volume ls failed:\n{r.stderr.decode()}"
        out = r.stdout.decode()

        # Header present.
        assert "NAME" in out and "GUEST PATH" in out and "MAC PATH" in out and "SIZE" in out

        # Both volumes listed, sorted by name (cache < data).
        i_cache = out.find("cache")
        i_data = out.find("data")
        assert i_cache != -1 and i_data != -1, f"missing volume(s):\n{out}"
        assert i_cache < i_data, f"volumes not sorted by name:\n{out}"

        # Guest paths.
        assert "/var/lib/data" in out
        assert "/var/lib/cache" in out

        # Sizes: data has one file (~2 bytes), cache is empty.
        # Regex is lenient about padding.
        assert re.search(r"data.+\d+\s?B", out), f"data size not shown as bytes:\n{out}"
        assert re.search(r"cache.+0\s?B", out), f"cache should be 0B:\n{out}"

        # The auto-managed primary workspace volume is also listed,
        # named after the workspace dir's basename and pointing at the
        # Mac cwd (guest path) / the primary volume's Mac-side storage.
        primary_name = workspace.path.name
        assert primary_name in out, (
            f"primary workspace volume {primary_name!r} missing from `devm "
            f"volume ls` output:\n{out}"
        )
        assert str(workspace.path) in out, (
            f"primary workspace guest path missing from output:\n{out}"
        )
        assert str(workspace.volume_path()) in out, (
            f"primary workspace Mac-side storage path missing from output:\n{out}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
