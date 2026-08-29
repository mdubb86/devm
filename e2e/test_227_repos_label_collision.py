"""227: repos: label collision is a validation error; explicit label: resolves it.

Task 7 (mutagen-volumes plan) added `validateLabels`, which walks the
flat mutagen-sync-label namespace shared by `repos:` and `volumes:`
entries and rejects any two entries that resolve to the same label. A
repo's default label is the bare-clone name derived from its URL
(`schema.BareCloneName`); an explicit `label:` overrides the
derivation.

Validation-only: `devm reconcile` fails at `config.Load`, before the
CLI ever talks to the VM or the daemon's reconcile endpoint, so no
sandbox is created. The passing half (explicit label: resolves the
collision) does go on to talk to the e2e daemon, same as any other
`devm reconcile` on a project that's never been started.

Pins:
1. Two `repos:` entries whose URLs derive the same bare-clone name
   (`proj`) fail `devm reconcile`; the error names both entries and
   mentions `label`.
2. Adding an explicit `label:` to one entry resolves the collision —
   `devm reconcile` then succeeds.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm

_REPOS_COLLIDING = {
    "a": {
        "url": "https://example.test/team/proj.git",
        "primary": True,
    },
    "b": {
        "url": "https://example.test/otherteam/proj.git",
    },
}


@pytest.mark.timeout(60)
def test_repos_label_collision_rejected_then_resolved(devm, workspace):
    workspace.write_devmyaml(no_repo=True, repos=_REPOS_COLLIDING)

    r = subprocess.run(
        [devm.path, "reconcile"], cwd=str(workspace.path),
        capture_output=True, timeout=30,
    )
    assert r.returncode != 0, (
        "reconcile should reject two repos: entries that derive the "
        f"same label; got exit 0. stdout={r.stdout.decode()!r}"
    )
    err = r.stderr.decode()
    assert "label" in err, f"error should mention 'label'; got:\n{err}"
    assert "repos.a" in err and "repos.b" in err, (
        f"error should name both colliding entries (repos.a, repos.b); got:\n{err}"
    )

    # Explicit label: on one entry resolves the collision.
    resolved = dict(_REPOS_COLLIDING)
    resolved["b"] = {**resolved["b"], "label": "proj-b"}
    workspace.write_devmyaml(no_repo=True, repos=resolved)

    r = subprocess.run(
        [devm.path, "reconcile"], cwd=str(workspace.path),
        capture_output=True, timeout=30,
    )
    # rc=0: no changes; rc=2: reconcile detected pending changes and
    # is prompting for confirmation — validation passed either way
    # (validation-failed is rc=1 with a schema error on stderr).
    assert r.returncode in (0, 2), (
        "reconcile should accept the config once the label collision is "
        f"resolved: rc={r.returncode} stdout={r.stdout.decode()!r} "
        f"stderr={r.stderr.decode()!r}"
    )
