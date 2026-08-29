"""Pin: `mutagen sync flush` on an already-paused session exits non-zero
with 'unable to flush session: session is paused' in stderr.

devm's StopPhase (internal/serviceapi/mutagen_sessions.go) unconditionally
calls flush then pause on every session. That worked as long as the
session was ACTIVELY syncing at stop-time — but when a session had
already been paused (e.g. after a daemon respawn, or on the second
`devm stop` in a row), flush spammed the daemon error log with:

  mutagen stop <project>: flush session <name>:
  mutagen sync flush <id>: exit 1:
  Error: unable to flush session: session is paused

This test pins BOTH facts:
  - non-zero exit
  - specific stderr string 'session is paused'
so devm's StopPhase can safely gate flush on `.paused == false` without
guessing at the error shape.
"""
from __future__ import annotations

import shutil
import tempfile
from pathlib import Path

import pytest

from helpers import mutagen_contract as mc

pytestmark = pytest.mark.contract


def test_flush_on_paused_session_errors_with_named_message():
    alpha = Path(tempfile.mkdtemp(prefix="mut-alpha-", dir="/tmp"))
    beta = Path(tempfile.mkdtemp(prefix="mut-beta-", dir="/tmp"))
    try:
        with mc.daemon() as data_dir:
            mc.run(
                ["sync", "create", "--name", "flush-paused", str(alpha), str(beta)],
                data_dir=data_dir,
            )
            session_id = mc.sync_list(data_dir)[0]["identifier"]

            mc.run(["sync", "pause", session_id], data_dir=data_dir)
            assert mc.sync_list(data_dir)[0]["paused"] is True, (
                "the pause step must land before we try to flush"
            )

            r = mc.run(
                ["sync", "flush", session_id],
                data_dir=data_dir,
                check=False,
            )
            assert r.returncode != 0, (
                f"expected non-zero exit for flush-on-paused; got rc=0 "
                f"stdout={r.stdout!r} stderr={r.stderr!r}"
            )
            assert "session is paused" in r.stderr, (
                f"expected 'session is paused' in stderr — devm's "
                f"StopPhase gate depends on this exact substring to "
                f"skip the flush spam. Got: {r.stderr!r}"
            )
    finally:
        shutil.rmtree(alpha, ignore_errors=True)
        shutil.rmtree(beta, ignore_errors=True)
