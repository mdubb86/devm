"""211: a URL-omitted primary repo derives its clone URL from
`git remote get-url origin` in the Mac cwd, and its mutagen sync label
defaults to the Mac cwd's basename.

Exercises the same shape as test_171 (skipped pending Task 28's fuller
rework), using the `repos:` map: `repos.main` declares only
`secret:`, no `url:`. resolveRepoLabel
(internal/serviceapi/mutagen_sessions.go) falls back to
`filepath.Base(macCwd)` for exactly this shape -- the URL-nil primary.
"""
from __future__ import annotations
import subprocess

import pytest

pytestmark = pytest.mark.devm


def _git_init_with_origin(path, origin_url: str) -> None:
    subprocess.run(["git", "init", "-q", str(path)], check=True)
    subprocess.run(["git", "-C", str(path), "remote", "add", "origin", origin_url], check=True)


@pytest.mark.timeout(300)
def test_cold_start_url_nil_derives(devm, workspace):
    origin_url = workspace.bare_repo_url()
    _git_init_with_origin(workspace.path, origin_url)

    # No `url:` -> devm must derive it from `origin`, and the label
    # falls back to the basename of the Mac cwd (workspace.path).
    workspace.write_devmyaml(repos={"main": {"secret": "e2e_default"}})

    label = workspace.path.name
    guest_dir = f"/home/devm/{label}"

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"derived-URL cold-start failed:\n{r.stderr.decode()}"

        r = subprocess.run(
            [devm.path, "shell", "--", "test", "-d", f"{guest_dir}/.git"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, (
            f"{guest_dir}/.git missing -- label should default to the Mac cwd's "
            f"basename ({label!r}) for a URL-nil primary:\n{r.stderr.decode()}"
        )

        r = subprocess.run(
            [devm.path, "shell", "--", "cat", f"{guest_dir}/README.md"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert r.stdout.decode().strip() == "bare"
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
