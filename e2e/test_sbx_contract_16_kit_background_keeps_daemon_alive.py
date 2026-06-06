"""kit: startup[N].background: true keeps the daemon alive past 15s.

A startup step declared with `background: true` runs a long-lived
heartbeat (writes a timestamp every 100ms to /tmp/daemon-trail). 15s
after sandbox bring-up, the trail must span >= 14s — meaning the step
is still running.

Historically (pre-sbx-0.31) the kit-flag killed the step at ~5s
(was Quirk #4); devm worked around it with shell-level `nohup ... &`
in internal/render/spec.go's backgroundWrap. Tier 1a simplification
dropped that workaround because sbx 0.31 fixed the quirk. This test
locks the fixed behavior.

Devm dependency: internal/render/spec.go's buildStartupStep emits
`background: true` directly. If sbx ever re-introduces the 5s kill,
this test fires and we know to restore the nohup workaround.
"""
from __future__ import annotations

import time

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(120)
def test_kit_background_true_keeps_daemon_alive_past_15s(sandbox_name):
    startup = [
        {
            "command": [
                "sh", "-c",
                "date +%s.%N > /tmp/daemon-start; "
                "while true; do date +%s.%N >> /tmp/daemon-trail; sleep 0.1; done",
            ],
            "user": "1000",
            "description": "heartbeat",
            "background": True,
        },
    ]
    with contract_sandbox(minimal_kit(startup=startup), sandbox_name):
        time.sleep(15)
        r = sbx_exec(
            sandbox_name, "sh", "-c",
            "cat /tmp/daemon-start; echo ===; tail -1 /tmp/daemon-trail",
        )
        assert r.returncode == 0, f"failed to read trail: {r.stderr.decode()}"
        parts = r.stdout.decode().split("===")
        assert len(parts) >= 2, f"unexpected trail output: {r.stdout.decode()!r}"
        start = float(parts[0].strip())
        last = float(parts[1].strip())
        span = last - start
        assert span >= 14, (
            f"daemon trail spans only {span:.2f}s; sbx may have re-introduced "
            f"the kit-flag 5s kill (Quirk #4). Restore the nohup workaround "
            f"in internal/render/spec.go's buildStartupStep."
        )
