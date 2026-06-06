"""lifecycle: a failing startup: step is SILENT on sbx side.

Probed 2026-06-06 against sbx 0.31. Result: sbx is silent on startup
failures — the analog of contract_02 does NOT hold for startup.

Observed:
  - install: ["true"] (no-op, passes)
  - startup: [{command: ["sh", "-c", "false"]}]
  - sbx run does NOT exit (kept running 90s+ until killed)
  - sbx ls shows the sandbox as STATUS=running
  - sbx exec NAME true returns 0 — exec path is healthy
  - sbx run stdout has zero diagnostic about the failed step

This is the inverse of contract_02 (install failure → loud rc + no
zombie). The asymmetry MATTERS for devm's failure-detection UX: a user
with a broken startup: step gets a working shell in a half-broken
sandbox with no signal that anything went wrong.

Devm dependency: shell.go's anchor ring buffer + handedOff defer can
only surface what sbx itself reports. Since sbx reports nothing here,
devm needs its own out-of-band detection — the marker-file pattern
(devm appends a final startup step that writes a marker; RunShell
checks the marker post-bringup; missing → fail loud with captured
output). Pinned-as-gap here; tracked for implementation in a follow-up
spec.

If this test ever flips to "rc != 0 / sandbox stopped / state == failed",
that's news: sbx fixed the silent-startup gap and the marker scheme can
be dropped. Update the assertions then.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers import sbx
from helpers.contract import contract_sandbox, minimal_kit

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(180)
def test_startup_failure_cold_start_is_silent(sandbox_name):
    spec = minimal_kit(
        install=["true"],  # install passes
        startup=[
            {
                "command": ["sh", "-c", "false"],  # startup exits 1
                "user": "1000",
                "description": "deliberately failing startup probe",
            }
        ],
    )

    # contract_sandbox waits for state=running + exec-ready. If sbx WERE
    # loud on startup failure, _wait_running or _wait_exec_ready would
    # time out (90s + 30s). The fact that it doesn't is itself evidence
    # of the silence we're pinning.
    with contract_sandbox(spec, sandbox_name) as ctx:
        # Pin 1: sandbox state is "running" despite the failed startup.
        assert sbx.sandbox_state(sandbox_name) == "running", (
            f"expected silent failure: sbx ls should report 'running' even "
            f"with a failed startup step; got {sbx.sandbox_state(sandbox_name)!r}.\n"
            f"captured sbx run output:\n{ctx.captured()}"
        )

        # Pin 2: exec works — sandbox is fully functional from the
        # outside despite the startup failure.
        r = subprocess.run(
            ["sbx", "exec", sandbox_name, "true"],
            capture_output=True, timeout=10,
        )
        assert r.returncode == 0, (
            f"expected silent failure: sbx exec true should succeed even "
            f"with a failed startup step; got rc={r.returncode} "
            f"stderr={r.stderr.decode()!r}"
        )

        # Pin 3: sbx run's captured output does NOT mention the startup
        # failure. If sbx ever starts reporting "startup step N exited 1"
        # in the run output, this assertion flips and we can drop the
        # marker scheme.
        captured = ctx.captured()
        assert "startup" not in captured.lower() or "failed" not in captured.lower(), (
            f"sbx run output unexpectedly mentions startup failure: "
            f"if sbx is now loud about startup failures, drop the marker "
            f"scheme.\nCaptured:\n{captured}"
        )
