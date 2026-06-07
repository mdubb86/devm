"""bash: `"$@" 2>&1 | s6-log <dir>` captures both streams, PIPESTATUS gives user rc.

Pins the two bash primitives the startup-supervision wrapper depends
on (docs/superpowers/specs/2026-06-07-startup-supervision-design.md):

  1. `cmd 2>&1 | s6-log <dir>` writes BOTH stdout and stderr from cmd
     into <dir>/current. Confirms that fd-dup at exec time + the pipe
     to s6-log captures the merged stream end-to-end on the shell base.
  2. `${PIPESTATUS[0]}` captures cmd's exit code (NOT s6-log's). This
     is load-bearing: the wrapper writes the user command's rc to
     <phase>-<N>.rc and uses it for the marker (and propagation),
     so it must read cmd's status, not s6-log's.

Devm dependency: wrap-fg.sh and wrap-bg.sh in the supervision design
use exactly this pipe shape. If either property breaks on this base,
the wrapper's stdout/stderr capture or rc handling is broken.

This test installs s6 (required for s6-log) and then probes both
properties from inside the sandbox using bash.
"""
from __future__ import annotations

import time

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(240)
def test_pipe_to_s6_log_captures_both_streams_and_pipestatus_works(sandbox_name):
    spec = minimal_kit(
        install=[
            "apt-get update",
            "DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends s6",
            "touch /tmp/install-done",
        ],
    )

    with contract_sandbox(spec, sandbox_name):
        # Wait for install: to complete (apt install s6 takes ~20s).
        deadline = time.monotonic() + 120
        while time.monotonic() < deadline:
            r = sbx_exec(sandbox_name, "test", "-f", "/tmp/install-done")
            if r.returncode == 0:
                break
            time.sleep(1.0)
        else:
            raise AssertionError("install: did not complete within 120s")

        # 1. cmd 2>&1 | s6-log writes both streams into <dir>/current.
        #    The probe cmd emits to stdout AND stderr; both lines must
        #    end up in the logdir.
        probe = (
            "rm -rf /tmp/probe-merge && mkdir -p /tmp/probe-merge && "
            "bash -c '{ echo OUTLINE; echo ERRLINE 1>&2; } "
            "2>&1 | s6-log -b n20 s1000000 T /tmp/probe-merge' && "
            "cat /tmp/probe-merge/current"
        )
        r = sbx_exec(sandbox_name, "sh", "-c", probe)
        assert r.returncode == 0, (
            f"basic 2>&1 | s6-log probe failed: stdout={r.stdout.decode()!r} "
            f"stderr={r.stderr.decode()!r}"
        )
        current = r.stdout.decode()
        assert "OUTLINE" in current, (
            f"stdout line missing from merged log; current=\n{current}"
        )
        assert "ERRLINE" in current, (
            f"stderr line missing from merged log (2>&1 didn't dup it into "
            f"the pipe); current=\n{current}"
        )

        # 2. PIPESTATUS[0] gives cmd's rc, not s6-log's.
        #    Probe: failing user cmd, successful s6-log. PIPESTATUS[0]
        #    must be the failure (7), even though s6-log succeeded (0).
        probe_rc = (
            "rm -rf /tmp/probe-rc && mkdir -p /tmp/probe-rc && "
            "bash -c 'set -o pipefail; "
            "( echo line; exit 7 ) 2>&1 | s6-log -b n20 s1000000 T /tmp/probe-rc; "
            "echo got=${PIPESTATUS[0]}'"
        )
        r = sbx_exec(sandbox_name, "sh", "-c", probe_rc)
        # outer rc is from `echo got=...`, which succeeds.
        assert r.returncode == 0, (
            f"PIPESTATUS probe outer command failed: stderr={r.stderr.decode()!r}"
        )
        out = r.stdout.decode()
        assert "got=7" in out, (
            f"PIPESTATUS[0] did not capture user cmd's rc=7; got: {out!r}. "
            f"The wrapper relies on PIPESTATUS[0] to write <phase>-<N>.rc; "
            f"if this shows the wrong rc, marker semantics break."
        )
