"""Mac-side: devm recipes asset ls / asset get returns the claude recipe's assets.

No VM cold-start needed — the recipes DB is cached locally and query
commands work with just the daemon running.
"""
import subprocess

import pytest

from helpers.devm import Devm

pytestmark = pytest.mark.devm


def test_recipes_asset_ls_and_get(devm: Devm, workspace) -> None:
    # Ensure the cache exists. `devm recipes list` fires lazy sync.
    r = subprocess.run(
        [devm.path, "recipes", "list"],
        capture_output=True, timeout=30, cwd=str(workspace.path),
    )
    assert r.returncode == 0, f"recipes list failed: {r.stderr.decode()}"

    # ls the claude recipe's assets.
    r = subprocess.run(
        [devm.path, "recipes", "asset", "ls", "tool/ai/claude"],
        capture_output=True, timeout=15, cwd=str(workspace.path),
    )
    assert r.returncode == 0, f"asset ls failed: {r.stderr.decode()}"
    listing = r.stdout.decode().strip().splitlines()
    assert len(listing) >= 5, f"expected >=5 assets, got: {listing!r}"
    paths = [line.rsplit("\t", 1)[-1] for line in listing]
    assert "skills/gdevm-guest.md" in paths
    assert "claude-local-mac.md" in paths

    # get one asset — bytes must be non-empty and start with a known
    # marker from the source content.
    r = subprocess.run(
        [devm.path, "recipes", "asset", "get", "tool/ai/claude", "skills/gdevm-guest.md"],
        capture_output=True, timeout=15, cwd=str(workspace.path),
    )
    assert r.returncode == 0, f"asset get failed: {r.stderr.decode()}"
    assert len(r.stdout) > 0
    assert b"gdevm propose" in r.stdout, "asset content missing expected marker"

    # invalid path → non-zero exit, actionable stderr.
    r = subprocess.run(
        [devm.path, "recipes", "asset", "get", "tool/ai/claude", "../etc/passwd"],
        capture_output=True, timeout=15, cwd=str(workspace.path),
    )
    assert r.returncode != 0
    assert b"invalid" in r.stderr.lower()

    # missing asset → non-zero exit, actionable stderr.
    r = subprocess.run(
        [devm.path, "recipes", "asset", "get", "tool/ai/claude", "skills/nonexistent.md"],
        capture_output=True, timeout=15, cwd=str(workspace.path),
    )
    assert r.returncode != 0
    assert b"asset" in r.stderr.lower() or b"not found" in r.stderr.lower()
