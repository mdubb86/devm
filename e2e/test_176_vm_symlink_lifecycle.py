"""176: `.vm` symlink lifecycle — self-healing, idempotent.

  1. Cold-start creates `.vm/` pointing at the primary volume storage.
  2. Removing `.vm` by hand, then `devm start` again (VM stopped ->
     cold-start path re-runs), recreates it.
  3. `.git/info/exclude`'s `/.vm` entry is never duplicated across
     repeated cold-starts, even one that starts from a hand-duplicated
     file.
"""
from __future__ import annotations
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(500)
def test_vm_symlink_lifecycle(devm, workspace):
    subprocess.run(["git", "init", "-q", str(workspace.path)], check=True)
    workspace.write_devmyaml()

    vm_link = workspace.path / ".vm"
    exclude_path = workspace.path / ".git" / "info" / "exclude"

    try:
        # 1. Cold-start creates .vm.
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert vm_link.is_symlink()
        assert vm_link.readlink() == workspace.volume_path()
        first_count = exclude_path.read_text().count("/.vm")
        assert first_count == 1, f"expected exactly one /.vm entry, got {first_count}"

        # 2. Remove .vm by hand; stop + start recreates it.
        vm_link.unlink()
        assert not vm_link.exists()

        r = subprocess.run(
            [devm.path, "stop", "--yes"], cwd=str(workspace.path),
            capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()

        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert vm_link.is_symlink(), ".vm was not recreated on restart"
        assert vm_link.readlink() == workspace.volume_path()
        # Recreation on an already-correct exclude file must not dupe.
        assert exclude_path.read_text().count("/.vm") == first_count

        # 3. Hand-duplicate the exclude entry, then cold-start again —
        # devm's own idempotency check (Contains, not count-based) must
        # not add a further copy on top of the hand-made duplicate.
        with exclude_path.open("a") as f:
            f.write("/.vm\n")
        duped_count = exclude_path.read_text().count("/.vm")
        assert duped_count == first_count + 1

        r = subprocess.run(
            [devm.path, "stop", "--yes"], cwd=str(workspace.path),
            capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()

        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert exclude_path.read_text().count("/.vm") == duped_count, (
            "cold-start added another /.vm entry on top of a hand-"
            "duplicated exclude file"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
