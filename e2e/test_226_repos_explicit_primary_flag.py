"""226: `primary: true` explicitly resolves primary determination when
every `repos:` entry declares a `url:`.

validateRepos (internal/schema/schema.go) determines primary either by
a lone `primary: true` entry or a lone URL-nil entry. When both
entries have URLs, only the explicit flag can resolve it -- and the
constraint that only the PRIMARY may have `volume: false` is the
observable proof that the flag was honored: rejecting on the flagged
entry's name proves the daemon considered it the primary, not the
other one.

Validation-only (mirrors test_227/test_228's style): `devm reconcile`
fails at config.Load, before any VM is created.
"""
from __future__ import annotations
import subprocess

import pytest

pytestmark = pytest.mark.devm

_REPOS_BASE = {
    "a": {
        "url": "https://example.test/team/proj-a.git",
        "primary": True,
    },
    "b": {
        "url": "https://example.test/team/proj-b.git",
    },
}


@pytest.mark.timeout(60)
def test_repos_explicit_primary_flag(devm, workspace):
    # repos.a is the explicit primary; giving it volume: false must be
    # rejected BY NAME -- if the daemon had resolved "b" as primary
    # instead, this constraint wouldn't fire on "a" at all.
    rejected = dict(_REPOS_BASE)
    rejected["a"] = {**rejected["a"], "volume": False}
    workspace.write_devmyaml(no_repo=True, repos=rejected)

    r = subprocess.run(
        [devm.path, "reconcile"], cwd=str(workspace.path),
        capture_output=True, timeout=30,
    )
    assert r.returncode != 0, (
        f"reconcile should reject the explicit primary having volume: false; "
        f"got exit 0. stdout={r.stdout.decode()!r}"
    )
    err = r.stderr.decode()
    assert "repos.a" in err, f"error should name the explicit primary (repos.a); got:\n{err}"
    assert "primary cannot have volume: false" in err, (
        f"expected the primary/volume-false rejection reason; got:\n{err}"
    )

    # Without volume: false, both URL'd entries + one primary: true
    # validates cleanly.
    workspace.write_devmyaml(no_repo=True, repos=_REPOS_BASE)

    r = subprocess.run(
        [devm.path, "reconcile"], cwd=str(workspace.path),
        capture_output=True, timeout=30,
    )
    # rc=0: no changes; rc=2: reconcile detected pending changes and
    # is prompting for confirmation — either is a validation-passed
    # state (validation-failed is rc=1 with a schema error on stderr).
    assert r.returncode in (0, 2), (
        f"reconcile should accept an explicit primary: true among "
        f"URL'd entries: rc={r.returncode} stdout={r.stdout.decode()!r} "
        f"stderr={r.stderr.decode()!r}"
    )
