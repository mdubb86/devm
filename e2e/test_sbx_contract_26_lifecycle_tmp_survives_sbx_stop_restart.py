"""lifecycle: /tmp SURVIVES `sbx stop` + `sbx run NAME` (no fresh-per-run).

Probed 2026-06-07. Observed: a file written to /tmp in one cold start
is STILL present after `sbx stop NAME` followed by `sbx run NAME`.
This means `sbx stop`/`run` is not a fresh-tmpfs-on-resume operation
— the container is paused/resumed (or stopped/restarted) with
filesystem state preserved end-to-end, including /tmp.

Devm dependency: the startup-supervision design
(docs/superpowers/specs/2026-06-07-startup-supervision-design.md)
needs marker freshness per `sbx run`. Since /tmp does NOT give us
free freshness, the design MUST add an explicit cleanup step at the
head of startup: that wipes /tmp/.devm/ before any user step runs:

    # built-in startup step #0 (devm-prepended)
    rm -rf /tmp/.devm
    mkdir -p /tmp/.devm

Without this, markers from a previous successful run would still be
present during a subsequent failed run, and devm would think the
new run succeeded too.

This test pins the SURVIVAL (not the reset) so that if sbx ever
flips behavior — wiping /tmp on stop/restart — we know about it
and can drop the cleanup-step indirection.
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers import sbx
from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(180)
def test_tmp_survives_sbx_stop_and_restart(sandbox_name):
    spec = minimal_kit()

    with contract_sandbox(spec, sandbox_name):
        # Cold-start: write a probe file to /tmp.
        r = sbx_exec(sandbox_name, "sh", "-c", "echo hello > /tmp/probe-tmp")
        assert r.returncode == 0, f"failed to write probe file: {r.stderr.decode()!r}"

        # Sanity check: present immediately.
        r = sbx_exec(sandbox_name, "cat", "/tmp/probe-tmp")
        assert r.returncode == 0 and r.stdout.decode().strip() == "hello"

        # sbx stop NAME.
        s = subprocess.run(["sbx", "stop", sandbox_name], capture_output=True, timeout=30)
        assert s.returncode == 0, f"sbx stop failed: {s.stderr.decode()!r}"

        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            if sbx.sandbox_state(sandbox_name) == "stopped":
                break
            time.sleep(0.5)
        assert sbx.sandbox_state(sandbox_name) == "stopped"

        # sbx run NAME — restart of existing sandbox.
        proc = subprocess.Popen(
            ["sbx", "run", sandbox_name],
            stdin=subprocess.DEVNULL, stdout=subprocess.DEVNULL, stderr=subprocess.PIPE,
        )
        try:
            deadline = time.monotonic() + 60
            while time.monotonic() < deadline:
                if sbx.sandbox_state(sandbox_name) == "running":
                    break
                time.sleep(0.5)
            assert sbx.sandbox_state(sandbox_name) == "running"

            # The contract pin: /tmp/probe-tmp MUST still be present.
            # If this flips to absent, sbx now wipes /tmp on restart and
            # the marker-cleanup step in startup-supervision can be
            # dropped.
            r = sbx_exec(sandbox_name, "cat", "/tmp/probe-tmp")
            assert r.returncode == 0, (
                f"/tmp/probe-tmp DISAPPEARED after `sbx stop` + `sbx run` — "
                f"sbx now wipes /tmp on restart. Good news: the explicit "
                f"`rm -rf /tmp/.devm` cleanup step in the startup-supervision "
                f"design can be removed. Update the design accordingly."
            )
            assert r.stdout.decode().strip() == "hello", (
                f"/tmp/probe-tmp content corrupted after restart: {r.stdout.decode()!r}"
            )
        finally:
            try:
                proc.kill()
                proc.wait(timeout=5)
            except Exception:
                pass
