"""Pin: mutagen's ignore syntax follows gitignore's dir-exclusion rule:
if `scratch/` excludes the directory, `!scratch/keep/` DOES NOT re-include
its child — the excluded parent's walk is skipped entirely, so
subordinate patterns never get a chance to override.

This is the semantics test_241 tripped over: users expect
    ignore: ["scratch/", "!scratch/keep/"]
to mean 'exclude scratch but keep scratch/keep' — that's how gitignore's
DOCS read at a glance, but the actual behavior in both git and mutagen
is that the excluded parent's tree is skipped. The correct expression is
    ignore: ["scratch/*", "!scratch/keep/"]
(exclude scratch's direct children, then re-include the specific one).

devm doesn't need to fix mutagen — but our docs need to describe this,
and this contract test is what makes the semantics observable in one
place.
"""
from __future__ import annotations

import shutil
import tempfile
import time
from pathlib import Path

import pytest

from helpers import mutagen_contract as mc

pytestmark = pytest.mark.contract


CONFIG_WITH_DIR_EXCLUSION = """\
sync:
  defaults:
    mode: two-way-resolved
    scanMode: accelerated
    ignore:
      vcs: false
      paths:
        - "scratch/"
        - "!scratch/keep/"
"""


def _wait_synced(target: Path, timeout: float = 5.0) -> None:
    """Wait for target to exist. Mutagen's flush command returns when a
    full sync cycle has completed, but the FS write races with the file
    stat we do afterwards — a short poll is safer than a bare stat."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if target.exists():
            return
        time.sleep(0.05)


def test_negation_does_not_reinclude_excluded_dir():
    alpha = Path(tempfile.mkdtemp(prefix="mut-alpha-", dir="/tmp"))
    beta = Path(tempfile.mkdtemp(prefix="mut-beta-", dir="/tmp"))
    cfg = Path(tempfile.mkstemp(suffix=".yml", prefix="mut-ignore-", dir="/tmp")[1])
    cfg.write_text(CONFIG_WITH_DIR_EXCLUSION)
    try:
        with mc.daemon() as data_dir:
            mc.run(
                [
                    "sync", "create",
                    "--configuration-file", str(cfg),
                    "--name", "ignore-neg",
                    str(alpha), str(beta),
                ],
                data_dir=data_dir,
            )
            session_id = mc.sync_list(data_dir)[0]["identifier"]

            # Write on alpha: excluded parent + supposedly-re-included child.
            (alpha / "scratch").mkdir()
            (alpha / "scratch" / "other.txt").write_text("excluded parent\n")
            (alpha / "scratch" / "keep").mkdir()
            (alpha / "scratch" / "keep" / "foo.txt").write_text("re-include attempt\n")
            (alpha / "normal.txt").write_text("proves sync is running\n")

            r = mc.run(["sync", "flush", session_id], data_dir=data_dir)
            assert r.returncode == 0, f"flush failed: {r.stderr!r}"

            # Sanity — the sibling non-ignored file DID sync. Without
            # this belt, a broken sync would look identical to a working
            # ignore rule.
            _wait_synced(beta / "normal.txt")
            assert (beta / "normal.txt").exists(), (
                f"normal.txt didn't sync — the whole sync is broken, "
                f"so the ignore assertion below can't distinguish "
                f"'correctly ignored' from 'nothing synced at all'."
            )

            # The pin: `!scratch/keep/` does NOT re-include, because the
            # `scratch/` exclusion skipped the walk into scratch.
            assert not (beta / "scratch" / "keep" / "foo.txt").exists(), (
                f"scratch/keep/foo.txt appeared on beta — mutagen has "
                f"changed its ignore-syntax semantics to allow re-including "
                f"a child of an excluded directory. gitignore compatibility "
                f"would be surprising here; check whether devm's docs need "
                f"updating."
            )
            # And the excluded sibling under scratch also stays off beta.
            assert not (beta / "scratch" / "other.txt").exists(), (
                f"scratch/other.txt on beta — the excluded parent's own "
                f"children are supposed to be excluded too."
            )
    finally:
        shutil.rmtree(alpha, ignore_errors=True)
        shutil.rmtree(beta, ignore_errors=True)
        cfg.unlink(missing_ok=True)
