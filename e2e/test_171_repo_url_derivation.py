"""171: repo.url derivation from the cwd's git `origin` remote.

Two sub-cases, each its own cold-start:
  - derived: `repo.url:` omitted, cwd is a git repo with an `origin`
    remote pointing at the fixture's bare repo -> devm derives the URL
    and clones it successfully.
  - override: `repo.url:` given explicitly, even though `origin` points
    at a URL that can't be cloned -> the explicit URL wins, proving
    override takes precedence over derivation (if devm ignored the
    explicit field and derived instead, this cold-start would fail
    against the bogus origin).
"""
from __future__ import annotations
import subprocess

import pytest

pytestmark = [
    pytest.mark.devm,
    pytest.mark.skip(reason="Task 28: pending mutagen-volumes fixture migration"),
]


def _git_init_with_origin(path, origin_url: str) -> None:
    subprocess.run(["git", "init", "-q", str(path)], check=True)
    subprocess.run(["git", "-C", str(path), "remote", "add", "origin", origin_url], check=True)


@pytest.mark.timeout(300)
def test_repo_url_derived_from_origin(devm, workspace):
    url = workspace.bare_repo_url()
    _git_init_with_origin(workspace.path, url)
    # No `repo.url:` -> devm must derive it from `origin`.
    workspace.write_devmyaml(repo={"secret": "e2e_default"})

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, (
            f"derived-URL cold-start failed:\n{r.stderr.decode()}"
        )
        assert (workspace.volume_path() / "README.md").exists()
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )


@pytest.mark.timeout(300)
def test_repo_url_explicit_overrides_origin(devm, workspace):
    real_url = workspace.bare_repo_url()
    decoy_origin = "file:///no/such/decoy-repo.git"
    _git_init_with_origin(workspace.path, decoy_origin)
    # Explicit repo.url: must win over the (bogus) origin remote.
    workspace.write_devmyaml(repo={"url": real_url, "secret": "e2e_default"})

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, (
            f"explicit repo.url cold-start failed (may indicate devm "
            f"derived from origin instead of using the explicit URL):\n"
            f"{r.stderr.decode()}"
        )
        assert (workspace.volume_path() / "README.md").exists()
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
