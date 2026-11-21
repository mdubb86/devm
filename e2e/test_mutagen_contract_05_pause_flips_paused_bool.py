"""Pin: `mutagen sync pause` flips the top-level `paused` bool to true.
`status` DOES NOT become the string 'paused' — it stays a transport
state like 'Disconnected' or 'Watching'. The two fields are orthogonal.

This is the exact behavior test_213 assumed wrong for months: devm's
SetupPhase resume-branch keyed off `existing.Status == \"paused\"` and
never fired, so a paused session was re-created instead of resumed on
next `devm start`. The real signal is `.paused == true`.

Failure mode this test locks: if a future mutagen release fuses the
two fields (e.g. status becoming 'Paused' when paused=true), the shape
devm's fixed resume logic depends on would silently drift.
"""
from __future__ import annotations

import shutil
import tempfile
from pathlib import Path

import pytest

from helpers import mutagen_contract as mc

pytestmark = pytest.mark.contract


def test_pause_flips_paused_true_but_not_status():
    alpha = Path(tempfile.mkdtemp(prefix="mut-alpha-", dir="/tmp"))
    beta = Path(tempfile.mkdtemp(prefix="mut-beta-", dir="/tmp"))
    try:
        with mc.daemon() as data_dir:
            mc.run(
                ["sync", "create", "--name", "pause-test", str(alpha), str(beta)],
                data_dir=data_dir,
            )
            rows = mc.sync_list(data_dir)
            assert len(rows) == 1
            session_id = rows[0]["identifier"]

            # Pre-pause: paused is false. Status can be anything active
            # (Watching, Scanning, Reconciling — depends on timing).
            assert rows[0]["paused"] is False, (
                f"pre-pause session must be paused=false, got "
                f"{rows[0]['paused']!r}"
            )

            # Pause and observe.
            r = mc.run(["sync", "pause", session_id], data_dir=data_dir)
            assert r.returncode == 0, f"sync pause failed: {r.stderr!r}"

            after = mc.sync_list(data_dir)
            assert len(after) == 1
            row = after[0]

            # The pin: paused flips to true.
            assert row["paused"] is True, (
                f"expected `paused=true` after sync pause; got "
                f"{row['paused']!r}. This is the field devm's resume "
                f"branch must key off — if it stops flipping, the "
                f"pause/resume cycle silently drops."
            )
            # And the anti-pin: status is NOT the literal 'paused'. devm's
            # buggy resume check used this branch; the contract test
            # documents that mutagen never produced that value.
            assert row["status"].lower() != "paused", (
                f"unexpected: status field is 'paused' (lowercase-insensitive). "
                f"Mutagen 0.18 exposes activity state in .status and "
                f"user-pause state in .paused as two ORTHOGONAL fields — "
                f"if that changes, devm's resume-branch needs to be "
                f"revisited. Got status={row['status']!r} paused={row['paused']!r}"
            )
    finally:
        shutil.rmtree(alpha, ignore_errors=True)
        shutil.rmtree(beta, ignore_errors=True)
