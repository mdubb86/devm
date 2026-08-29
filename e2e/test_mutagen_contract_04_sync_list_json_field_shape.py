"""Pin: field names + types in `mutagen sync list --template '{{json .}}'`.

devm's mutagen.CLI.SyncList parses this JSON into
    type syncSessionJSON struct {
        Identifier string `json:"identifier"`
        Name       string `json:"name"`
        Status     string `json:"status"`
    }

Any rename or type change (e.g. `.status` becoming a nested object)
silently breaks the parser. devm's Go tests fake this shape; only a
contract test hitting real mutagen catches upstream drift.

Local-to-local sync alpha=/tmp/A beta=/tmp/B is cheap: no ssh, no agent
install, session goes straight to a syncing state. All four core fields
appear immediately.

Bonus pin: the JSON must expose `paused` as its OWN top-level key (not a
nested field on `.status`). devm's resume logic keyed off
`existing.Status == "paused"` incorrectly for months — the real signal is
`.paused: true`, and `.status` is the transport/activity state (e.g.
`Watching`, `Disconnected`). This test locks that split explicitly so
future refactors of SetupPhase's resume branch have something to point at.
"""
from __future__ import annotations

import shutil
import tempfile
from pathlib import Path

import pytest

from helpers import mutagen_contract as mc

pytestmark = pytest.mark.contract


def _make_endpoints() -> tuple[Path, Path]:
    """Return (alpha, beta) as fresh /tmp directories. Short paths so the
    mutagen daemon can bind its own socket — see helpers.short_data_dir."""
    alpha = Path(tempfile.mkdtemp(prefix="mut-alpha-", dir="/tmp"))
    beta = Path(tempfile.mkdtemp(prefix="mut-beta-", dir="/tmp"))
    return alpha, beta


def test_sync_list_json_field_shape():
    alpha, beta = _make_endpoints()
    try:
        with mc.daemon() as data_dir:
            r = mc.run(
                ["sync", "create", "--name", "contract-test", str(alpha), str(beta)],
                data_dir=data_dir,
            )
            assert r.returncode == 0, f"sync create failed: {r.stderr!r}"

            rows = mc.sync_list(data_dir)
            assert len(rows) == 1, (
                f"expected exactly one row after creating one session, "
                f"got {rows!r}"
            )
            row = rows[0]

            # Core fields devm's syncSessionJSON parses.
            assert isinstance(row.get("identifier"), str) and row["identifier"], (
                f"`identifier` must be a non-empty string; got "
                f"{row.get('identifier')!r} in {row!r}"
            )
            assert row.get("name") == "contract-test", (
                f"`name` must round-trip the --name arg verbatim; got "
                f"{row.get('name')!r}"
            )
            assert isinstance(row.get("status"), str), (
                f"`status` must be a string (mutagen's Status enum's "
                f"stringified form). Got {row.get('status')!r} — a nested "
                f"object here would silently break devm's SyncList parser."
            )

            # The paused/status split. `paused` is a top-level bool and
            # is orthogonal to `status`. A watching session has
            # paused=false and status="Watching" (or similar transient
            # states); a paused session gets paused=true and status
            # becomes the disconnected/idle transport state.
            assert "paused" in row, (
                f"expected top-level `paused` boolean in sync list JSON — "
                f"this is the field devm's resume-branch logic MUST key "
                f"off (not `.status == \"paused\"`, which is where the "
                f"real-world bug crept in). Got row: {row!r}"
            )
            assert isinstance(row["paused"], bool), (
                f"`paused` must be a bool, not str/int; got "
                f"{row['paused']!r} ({type(row['paused']).__name__})"
            )
            assert row["paused"] is False, (
                f"a freshly-created session must be paused=false — the "
                f"resume path in SetupPhase relies on this default. Got "
                f"paused={row['paused']!r}"
            )
    finally:
        shutil.rmtree(alpha, ignore_errors=True)
        shutil.rmtree(beta, ignore_errors=True)
