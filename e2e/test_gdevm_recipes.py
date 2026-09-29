"""Guest-side: gdevm recipes list/get/asset ls/asset get succeed against
the daemon's cached DB via the softnet route.

The daemon (`internal/serviceapi/propose.go`) reads recipes from
`recipes.CacheDir()/recipes.db`, which for the e2e launchd user resolves
to `~/.cache/devm/recipes.db`. Unlike the Mac-side CLI test
(test_recipes_asset_query.py), the DEVM_RECIPES_CACHE_DIR env override
does not reach the daemon — launchd owns its environment. So this
fixture seeds the daemon's real cache path from source, backing up any
pre-existing file and restoring it on teardown, so the assertions run
against exactly the current source tree (immune to release lag) without
polluting the shared cache.

Backups live under ~/.cache/devm rather than pytest's tmp_path so a
hard interrupt (SIGKILL of the runner, machine reboot) still lets us
restore. An atexit handler runs the same restore path as the fixture's
finally, and the fixture removes the backup files after a successful
restore so a later run doesn't clobber a fresh cache.
"""
import atexit
import os
import shutil
import subprocess
from datetime import datetime, timezone
from pathlib import Path
from typing import Iterator

import pytest

from helpers.devm import Devm
from helpers.workspace import Workspace

pytestmark = pytest.mark.devm


REPO_ROOT = Path(__file__).resolve().parents[1]


class _CacheBackup:
    """Save/restore the daemon's shared recipes cache around a test.

    The two files (recipes.db and recipes.lastcheck) are saved under
    ~/.cache/devm as `<name>.testbackup.<pid>` so they survive a hard
    kill of the pytest process. `restore()` is idempotent so it is safe
    to call from both a `finally` block and an atexit handler.
    """

    def __init__(self, db_path: Path, lastcheck_path: Path) -> None:
        self.db_path = db_path
        self.lastcheck_path = lastcheck_path
        pid = os.getpid()
        self.backup_db = db_path.with_name(f"recipes.db.testbackup.{pid}")
        self.backup_lc = lastcheck_path.with_name(
            f"recipes.lastcheck.testbackup.{pid}"
        )
        self.had_db = db_path.exists()
        self.had_lc = lastcheck_path.exists()

    def save(self) -> None:
        if self.had_db:
            shutil.copy2(self.db_path, self.backup_db)
        if self.had_lc:
            shutil.copy2(self.lastcheck_path, self.backup_lc)

    def restore(self) -> None:
        # Restore db.
        if self.had_db:
            if self.backup_db.exists():
                shutil.copy2(self.backup_db, self.db_path)
                self.backup_db.unlink()
        else:
            self.db_path.unlink(missing_ok=True)
            self.backup_db.unlink(missing_ok=True)
        # Restore lastcheck.
        if self.had_lc:
            if self.backup_lc.exists():
                shutil.copy2(self.backup_lc, self.lastcheck_path)
                self.backup_lc.unlink()
        else:
            self.lastcheck_path.unlink(missing_ok=True)
            self.backup_lc.unlink(missing_ok=True)


@pytest.fixture
def seed_daemon_recipes_db() -> Iterator[None]:
    """Build recipes.db from the source tree into the daemon's real cache
    path (`~/.cache/devm/recipes.db`), stamp lastcheck fresh, run the
    test, then restore whatever was there before. The daemon opens the
    file each request (see internal/serviceapi/propose.go), so a swap
    around the test suffices — no daemon restart needed."""
    cache_dir = Path.home() / ".cache" / "devm"
    cache_dir.mkdir(parents=True, exist_ok=True)
    db_path = cache_dir / "recipes.db"
    lastcheck_path = cache_dir / "recipes.lastcheck"

    backup = _CacheBackup(db_path, lastcheck_path)
    backup.save()
    # atexit fires even on SIGTERM / unhandled exception, covering the
    # case where the fixture's finally block never runs.
    atexit.register(backup.restore)

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
    # RFC3339 with `Z` (UTC) matches Go's time.RFC3339 layout exactly.
    lastcheck_path.write_text(
        datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
    )

    try:
        yield
    finally:
        backup.restore()


def test_gdevm_recipes(
    workspace: Workspace, devm: Devm, seed_daemon_recipes_db: None,
) -> None:
    workspace.write_devmyaml()
    devm.approve()
    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=180,
    )
    assert r.returncode == 0, f"devm start failed: {r.stderr.decode()}"

    # recipes list — expect JSON.
    r = subprocess.run(
        [devm.path, "exec", "gdevm", "recipes", "list"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 0, f"gdevm recipes list failed: {r.stderr.decode()}"
    assert r.stdout.strip().startswith(b"["), (
        f"expected JSON array, got {r.stdout[:100]!r}"
    )

    # recipes get — markdown body.
    r = subprocess.run(
        [devm.path, "exec", "gdevm", "recipes", "get", "tool/ai/claude"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 0, f"gdevm recipes get failed: {r.stderr.decode()}"
    assert b"Claude Code" in r.stdout, "recipe body missing expected marker"

    # recipes asset ls.
    r = subprocess.run(
        [devm.path, "exec", "gdevm", "recipes", "asset", "ls", "tool/ai/claude"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 0, f"gdevm recipes asset ls failed: {r.stderr.decode()}"
    assert b"gdevm-guest.md" in r.stdout, f"missing expected asset: {r.stdout!r}"

    # recipes asset get — raw bytes.
    r = subprocess.run(
        [devm.path, "exec", "gdevm", "recipes", "asset", "get",
         "tool/ai/claude", "skills/gdevm-guest.md"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 0, f"gdevm recipes asset get failed: {r.stderr.decode()}"
    assert b"gdevm propose" in r.stdout, "asset content missing expected marker"

    # invalid path → exit 2 (daemon 400 → runner maps to 2).
    r = subprocess.run(
        [devm.path, "exec", "gdevm", "recipes", "asset", "get",
         "tool/ai/claude", "../etc/passwd"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 2, (
        f"expected exit 2 for invalid path, got {r.returncode}: {r.stderr.decode()}"
    )

    # missing asset → exit 3 (daemon 404 → runner maps to 3).
    r = subprocess.run(
        [devm.path, "exec", "gdevm", "recipes", "asset", "get",
         "tool/ai/claude", "skills/nope.md"],
        cwd=str(workspace.path), capture_output=True, timeout=30,
    )
    assert r.returncode == 3, (
        f"expected exit 3 for missing asset, got {r.returncode}: {r.stderr.decode()}"
    )
