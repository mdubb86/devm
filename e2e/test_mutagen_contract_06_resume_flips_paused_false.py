"""Pin: `mutagen sync resume` flips `.paused` back to false.

devm's SetupPhase resume branch, once corrected to key off `.paused`,
must see this flip on the next `sync list` — otherwise resume looks
like a no-op and the session sits in the paused state until a manual
intervention.

Complements contract 05 (pause direction) — the pair pins the full
pause/resume cycle.
"""
from __future__ import annotations

import shutil
import tempfile
from pathlib import Path

import pytest

from helpers import mutagen_contract as mc

pytestmark = pytest.mark.contract


def test_resume_flips_paused_false():
    alpha = Path(tempfile.mkdtemp(prefix="mut-alpha-", dir="/tmp"))
    beta = Path(tempfile.mkdtemp(prefix="mut-beta-", dir="/tmp"))
    try:
        with mc.daemon() as data_dir:
            mc.run(
                ["sync", "create", "--name", "resume-test", str(alpha), str(beta)],
                data_dir=data_dir,
            )
            session_id = mc.sync_list(data_dir)[0]["identifier"]

            mc.run(["sync", "pause", session_id], data_dir=data_dir)
            assert mc.sync_list(data_dir)[0]["paused"] is True, (
                "pause step must land — resume test depends on it"
            )

            r = mc.run(["sync", "resume", session_id], data_dir=data_dir)
            assert r.returncode == 0, f"sync resume failed: {r.stderr!r}"

            row = mc.sync_list(data_dir)[0]
            assert row["paused"] is False, (
                f"expected `paused=false` after sync resume; got "
                f"{row['paused']!r}. If resume ever stops flipping this "
                f"bit, devm's SetupPhase will keep re-resuming a session "
                f"that stays paused."
            )
    finally:
        shutil.rmtree(alpha, ignore_errors=True)
        shutil.rmtree(beta, ignore_errors=True)
