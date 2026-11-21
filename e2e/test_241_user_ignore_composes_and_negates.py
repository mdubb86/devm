"""241: a repo's `ignore:` list composes with internal/mutagen.
DefaultIgnores, and a `!pattern` negation un-ignores a subpath.

ComposeConfig (internal/mutagen/config_file.go) appends the entry's
own `ignore:` list AFTER DefaultIgnores, so a later `!scratch/keep/`
un-ignores that one subdirectory.

CRITICAL: mutagen honors the gitignore rule that an excluded parent
directory is not walked, so a bare `scratch/` exclusion makes any
`!scratch/keep/` un-ignore ineffective. To re-include a specific
subdirectory, exclude the PARENT'S CHILDREN with `scratch/*` (leaves
the parent walkable) and then negate the specific child. Pinned in
e2e/test_mutagen_contract_11.

Three sub-checks in one flush:
  a. `scratch/other.txt` (matched by `scratch/*`) does not sync.
  b. `scratch/keep/foo.txt` (under the negated `!scratch/keep/`) DOES
     sync.
  c. `apps/web/.next/cache/cachefile.txt` (nested, matched by the
     DEFAULT `**/.next/cache/` pattern regardless of nesting depth)
     does not sync.
A tracked control file proves sync itself is working.
"""
from __future__ import annotations
import subprocess

import pytest

from helpers.mutagen_e2e import mirror_path, session_prefix, sync_flush, sync_list

pytestmark = pytest.mark.devm


@pytest.mark.timeout(240)
def test_user_ignore_composes_and_negates(devm, workspace):
    workspace.write_devmyaml(
        repos={
            "main": {
                "url": workspace.bare_repo_url(),
                "primary": True,
                "ignore": ["scratch/*", "!scratch/keep/"],
            },
        },
    )
    label = workspace.bare_repo_label()

    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=180,
        )
        assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

        sessions = sync_list(session_prefix(workspace.vm_name))
        assert len(sessions) == 1, f"expected exactly one session, got {sessions}"
        session_id = sessions[0]["identifier"]

        base = f"/home/devm/{label}"
        script = (
            f"mkdir -p {base}/scratch/keep {base}/apps/web/.next/cache\n"
            f"echo a > {base}/scratch/other.txt\n"
            f"echo b > {base}/scratch/keep/foo.txt\n"
            f"echo c > {base}/apps/web/.next/cache/cachefile.txt\n"
            f"echo tracked > {base}/control.txt\n"
        )
        r = subprocess.run(
            [devm.path, "shell", "--", "sh", "-c", script],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"guest write failed:\n{r.stderr.decode()}"

        r = sync_flush(session_id)
        assert r.returncode == 0, f"mutagen sync flush failed:\n{r.stderr}"

        mirror = mirror_path(workspace.vm_name, label)

        assert (mirror / "control.txt").exists(), (
            f"control.txt (not covered by any ignore pattern) should have "
            f"synced to {mirror} -- if missing, sync itself may be broken"
        )

        assert not (mirror / "scratch" / "other.txt").exists(), (
            f"scratch/other.txt should be excluded by the user ignore "
            f"'scratch/*' but appeared at {mirror}"
        )

        assert (mirror / "scratch" / "keep" / "foo.txt").exists(), (
            f"scratch/keep/foo.txt should have synced — 'scratch/*' leaves "
            f"the parent dir walkable so '!scratch/keep/' can re-include "
            f"this subdir (contract 11 pins the walkable-parent rule)"
        )
        assert (mirror / "scratch" / "keep" / "foo.txt").read_text().strip() == "b"

        assert not (mirror / "apps" / "web" / ".next" / "cache" / "cachefile.txt").exists(), (
            f"apps/web/.next/cache/cachefile.txt should be excluded by the "
            f"default '**/.next/cache/' pattern (nested, not just root-level) "
            f"but appeared at {mirror}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
