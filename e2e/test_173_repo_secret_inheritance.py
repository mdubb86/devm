"""173: each `repos:` entry declares its own `secret:` independently —
there is no top-level `repo.secret:` for a secondary to fall back to.

Two entries name two DIFFERENT secrets ("s1", "s2") with no shared
value between them, proving there's no hidden inheritance/fallback
wiring one entry's secret onto the other — if there were, this config
would still validate and hydrate the same way, so the real proof is
structural: RepoConfig.Secret (internal/schema/repo.go) is a plain
independent field on every `repos:` entry, primary and secondary
alike, with no top-level equivalent left in Config for anything to
inherit from.

Validation-only pin (no actual clone): the fixture default's public
github endpoints don't accept the placeholder tokens iron-proxy
substitutes, so exercising this at the clone layer would fail for
reasons unrelated to secret-inheritance. `devm validate` runs schema
validation (secret-resolution included) and stops before touching the
VM — that's exactly the layer this test needs.
"""
from __future__ import annotations
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(60)
def test_secrets_declared_independently_per_entry(devm, workspace):
    for name in ("s1", "s2"):
        subprocess.run(
            [devm.path, "secret", "set", name],
            cwd=str(workspace.path),
            input=f"{name}-value\n".encode(),
            capture_output=True, timeout=15, check=True,
        )

    workspace.write_devmyaml(
        no_repo=True,
        repos={
            "main": {
                "url": "https://example.test/team/proj-a.git",
                "secret": "s1",
                "primary": True,
            },
            "secondary": {
                "url": "https://example.test/team/proj-b.git",
                "secret": "s2",
                "volume": True,
            },
        },
    )

    r = subprocess.run(
        [devm.path, "validate"], cwd=str(workspace.path),
        capture_output=True, timeout=15,
    )
    # rc=0: schema (including secret resolution) valid. rc=1 would
    # mean one of the two independently-named secrets failed to
    # resolve — the exact regression this test guards against.
    assert r.returncode == 0, (
        f"devm validate should accept two entries with independent, "
        f"individually-set secrets. rc={r.returncode} "
        f"stdout={r.stdout.decode()!r} stderr={r.stderr.decode()!r}"
    )
