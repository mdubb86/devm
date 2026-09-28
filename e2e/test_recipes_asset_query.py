"""Mac-side: devm recipes asset ls / asset get returns the claude recipe's assets.

Hermetic: builds a fresh recipes.db from the source tree into a
test-owned cache dir, stamps recipes.lastcheck fresh so lazyEnsureCache
short-circuits (no GitHub round trip), and points every `devm recipes`
invocation at that cache via DEVM_RECIPES_CACHE_DIR.
"""
import os
import subprocess
from datetime import datetime, timezone
from pathlib import Path

import pytest

from helpers.devm import Devm
from helpers.workspace import Workspace

pytestmark = pytest.mark.devm


REPO_ROOT = Path(__file__).resolve().parents[1]


def test_recipes_asset_ls_and_get(
    devm: Devm, workspace: Workspace, tmp_path: Path
) -> None:
    # Build a fresh recipes.db from the current source tree into a
    # test-owned cache dir, then stamp lastcheck fresh so lazyEnsureCache
    # is a no-op — no GitHub round trip needed.
    cache_dir = tmp_path / "recipes-cache"
    cache_dir.mkdir()
    db_path = cache_dir / "recipes.db"
    r = subprocess.run(
        ["go", "run", "./tools/build-recipes-db",
         "-src", "recipes",
         "-out", str(db_path),
         "-version", "recipes-vtest"],
        cwd=str(REPO_ROOT), capture_output=True, timeout=120,
    )
    assert r.returncode == 0, (
        f"build-recipes-db failed:\nstdout: {r.stdout.decode()}\n"
        f"stderr: {r.stderr.decode()}"
    )
    # RFC3339 with `Z` (UTC) matches Go's time.RFC3339 layout exactly;
    # avoids the microseconds / colon-vs-no-colon offset traps of
    # Python's default isoformat/%z.
    (cache_dir / "recipes.lastcheck").write_text(
        datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    )

    env = os.environ.copy()
    env["DEVM_RECIPES_CACHE_DIR"] = str(cache_dir)

    # Sanity check: list returns something against the seeded DB.
    r = subprocess.run(
        [devm.path, "recipes", "list"],
        cwd=str(workspace.path), capture_output=True, timeout=30, env=env,
    )
    assert r.returncode == 0, f"recipes list failed: {r.stderr.decode()}"

    # ls the claude recipe's assets — five files shipped in
    # recipes/ai/claude-assets/.
    r = subprocess.run(
        [devm.path, "recipes", "asset", "ls", "tool/ai/claude"],
        cwd=str(workspace.path), capture_output=True, timeout=15, env=env,
    )
    assert r.returncode == 0, f"asset ls failed: {r.stderr.decode()}"
    listing = r.stdout.decode().strip().splitlines()
    assert len(listing) == 5, f"expected 5 assets, got: {listing!r}"
    paths = [line.rsplit("\t", 1)[-1] for line in listing]
    assert "skills/gdevm-guest.md" in paths
    assert "claude-local-mac.md" in paths

    # get one asset — bytes must be non-empty and contain a known
    # marker from the source content.
    r = subprocess.run(
        [devm.path, "recipes", "asset", "get", "tool/ai/claude", "skills/gdevm-guest.md"],
        cwd=str(workspace.path), capture_output=True, timeout=15, env=env,
    )
    assert r.returncode == 0, f"asset get failed: {r.stderr.decode()}"
    assert len(r.stdout) > 0
    assert b"gdevm propose" in r.stdout, "asset content missing expected marker"

    # invalid path → non-zero exit, actionable stderr.
    r = subprocess.run(
        [devm.path, "recipes", "asset", "get", "tool/ai/claude", "../etc/passwd"],
        cwd=str(workspace.path), capture_output=True, timeout=15, env=env,
    )
    assert r.returncode != 0
    assert b"invalid" in r.stderr.lower()

    # missing asset → non-zero exit, actionable stderr.
    r = subprocess.run(
        [devm.path, "recipes", "asset", "get", "tool/ai/claude", "skills/nonexistent.md"],
        cwd=str(workspace.path), capture_output=True, timeout=15, env=env,
    )
    assert r.returncode != 0
    assert b"asset" in r.stderr.lower() or b"not found" in r.stderr.lower()
