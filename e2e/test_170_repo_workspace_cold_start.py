"""170: primary-only repo workspace hydrates on cold-start.

Pins:
1. Cold-start with `project.repo:` clones into the primary volume's
   Mac-side storage (the fixture's default `repo:` block points at a
   hermetic local `file://` bare repo).
2. `.vm/` symlink appears in Mac cwd, pointing at that storage.
3. `.git/info/exclude` contains `/.vm`.
4. Guest sees the cloned content at $WORKSPACE.

The workspace dir must be a real git repo for .git/info/exclude to
apply (EnsureGitExclude no-ops when .git doesn't exist), so this test
`git init`s it before cold-start.
"""
from __future__ import annotations
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_repo_workspace_cold_start(devm, workspace, sandbox_name):
    subprocess.run(["git", "init", "-q", str(workspace.path)], check=True)

    workspace.write_devmyaml()  # fixture's default repo: block is enough

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, r.stderr.decode()

        # Volume storage populated with clone contents.
        assert (workspace.volume_path() / "README.md").exists(), (
            f"primary volume storage {workspace.volume_path()} missing "
            f"cloned README.md"
        )

        # .vm symlink present, target correct.
        vm_link = workspace.path / ".vm"
        assert vm_link.is_symlink(), f"{vm_link} is not a symlink"
        assert vm_link.readlink() == workspace.volume_path()

        # .git/info/exclude has /.vm.
        exclude = (workspace.path / ".git" / "info" / "exclude").read_text()
        assert "/.vm" in exclude

        # Guest sees the cloned content at $WORKSPACE.
        r = subprocess.run(
            [devm.path, "shell", "--", "sh", "-c", 'cat "$WORKSPACE/README.md"'],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert r.stdout.decode().strip() == "bare"
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
