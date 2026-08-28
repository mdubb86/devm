"""173: each `repos:` entry declares its own `secret:` independently --
there is no top-level `repo.secret:` for a secondary to fall back to.

Two entries name two DIFFERENT secrets ("s1", "s2") with no shared
value between them, proving there's no hidden inheritance/fallback
wiring one entry's secret onto the other -- if there were, this
config would still validate and hydrate the same way, so the real
proof is structural: RepoConfig.Secret (internal/schema/repo.go) is a
plain independent field on every `repos:` entry, primary and
secondary alike, with no top-level equivalent left in Config for
anything to inherit from.

Local file:// clones ignore the substituted Authorization header
entirely, so the observable pin is: both entries validate and hydrate
successfully under independently-named secrets, not the literal
on-wire header.
"""
from __future__ import annotations
import subprocess

import pytest

pytestmark = pytest.mark.devm


def _make_bare_repo(tmp_path_factory, marker: str) -> str:
    work = tmp_path_factory.mktemp(f"{marker}-work")
    subprocess.run(["git", "-C", str(work), "init", "-q"], check=True)
    (work / f"{marker}.txt").write_text(f"{marker}\n")
    subprocess.run(["git", "-C", str(work), "add", "."], check=True)
    subprocess.run(
        ["git", "-C", str(work), "-c", "user.email=e2e@e2e", "-c", "user.name=e2e",
         "commit", "-q", "-m", "init"],
        check=True,
    )
    bare = tmp_path_factory.mktemp(f"{marker}-bare") / "repo.git"
    subprocess.run(["git", "clone", "--bare", "-q", str(work), str(bare)], check=True)
    return f"file://{bare}"


@pytest.mark.timeout(300)
def test_secrets_declared_independently_per_entry(devm, workspace, tmp_path_factory):
    secondary_url = _make_bare_repo(tmp_path_factory, "secondary")
    secondary_label = "repo"  # BareCloneName of ".../repo.git"
    primary_label = f"{workspace.path.name}-repo"

    for name in ("s1", "s2"):
        subprocess.run(
            [devm.path, "secret", "set", name],
            cwd=str(workspace.path),
            input=f"{name}-value\n".encode(),
            capture_output=True, timeout=15, check=True,
        )

    workspace.write_devmyaml(
        repos={
            "main": {"url": workspace.bare_repo_url(), "secret": "s1", "primary": True},
            "secondary": {"url": secondary_url, "secret": "s2", "volume": True},
        },
    )

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        r = subprocess.run(
            [devm.path, "shell", "--", "cat", f"/home/devm/{primary_label}/README.md"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert r.stdout.decode().strip() == "bare"

        r = subprocess.run(
            [devm.path, "shell", "--", "cat", f"/home/devm/{secondary_label}/secondary.txt"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, r.stderr.decode()
        assert r.stdout.decode().strip() == "secondary"
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
