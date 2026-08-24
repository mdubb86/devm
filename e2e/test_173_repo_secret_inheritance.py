"""173: secondary volumes inherit the top-level repo.secret unless they
declare their own.

Top-level `repo.secret: s1`. Secondary `seca` omits `secret:` entirely
(must inherit `s1` — schema validation would reject it otherwise,
since a secondary with neither its own secret nor a top-level one to
fall back on is a config error). Secondary `secb` declares its own
`secret: s2`, overriding the inherited value.

Local file:// clones ignore the substituted Authorization header
entirely, so the observable pin here is: both secondaries validate
and hydrate successfully under inherited vs. explicit secret naming —
not the literal on-wire header, which no local test can see.
"""
from __future__ import annotations
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_secret_inheritance_and_override(devm, workspace):
    url = workspace.bare_repo_url()

    for name in ("s1", "s2"):
        subprocess.run(
            [devm.path, "secret", "set", name],
            cwd=str(workspace.path),
            input=f"{name}-value\n".encode(),
            capture_output=True, timeout=15, check=True,
        )

    workspace.write_devmyaml(
        repo={"url": url, "secret": "s1"},
        volumes={
            "seca": {"path": "/mnt/seca", "repo": {"url": url}},
            "secb": {"path": "/mnt/secb", "repo": {"url": url, "secret": "s2"}},
        },
    )

    try:
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        for guest_path, vol_name in (("/mnt/seca", "seca"), ("/mnt/secb", "secb")):
            r = subprocess.run(
                [devm.path, "shell", "--", "ls", guest_path],
                cwd=str(workspace.path), capture_output=True, timeout=60,
            )
            assert r.returncode == 0, f"{vol_name}: {r.stderr.decode()}"
            assert "README.md" in r.stdout.decode(), f"{vol_name} not hydrated"
            assert (workspace.volume_path(vol_name) / "README.md").exists()
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
