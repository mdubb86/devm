"""bash: `"$@" 2>&1 | s6-log <dir>` captures both streams, PIPESTATUS gives user rc.

Pins the two bash primitives the startup-supervision wrapper depends
on (internal design notes):

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

This test stages the embedded static s6-log binary (from s6-overlay
v3.2.0.2, embedded by devm at .devm/scripts/s6-log via go:embed) in
the workspace and probes both properties from inside the sandbox.
No apt install s6 needed — the static binary has no runtime deps.
"""
from __future__ import annotations

import os
import shutil
import subprocess
import tempfile

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


def _embedded_s6_log_path() -> str:
    here = os.path.dirname(os.path.abspath(__file__))
    repo_root = os.path.dirname(here)
    arch = subprocess.run(["uname", "-m"], capture_output=True, check=True).stdout.decode().strip()
    arch_map = {"arm64": "arm64", "aarch64": "arm64", "x86_64": "amd64"}
    suffix = arch_map.get(arch, arch)
    return os.path.join(repo_root, "internal", "scripts", f"s6-log.linux-{suffix}")


@pytest.mark.timeout(120)
def test_pipe_to_s6_log_captures_both_streams_and_pipestatus_works(sandbox_name):
    s6log = _embedded_s6_log_path()
    assert os.path.exists(s6log), f"embedded s6-log not found at {s6log}"

    ws = tempfile.mkdtemp(prefix="probe-c28-ws-")
    try:
        shutil.copy(s6log, os.path.join(ws, "s6-log"))
        os.chmod(os.path.join(ws, "s6-log"), 0o755)
        s6log_in_sbx = f"{ws}/s6-log"

        spec = minimal_kit(install=["true"])
        with contract_sandbox(spec, sandbox_name, workspace=ws):
            # 1. cmd 2>&1 | s6-log writes both streams into <dir>/current.
            #    The probe cmd emits to stdout AND stderr; both lines must
            #    end up in the logdir.
            probe = (
                "rm -rf /tmp/probe-merge && mkdir -p /tmp/probe-merge && "
                f"bash -c '{{ echo OUTLINE; echo ERRLINE 1>&2; }} "
                f"2>&1 | {s6log_in_sbx} -b n20 s1000000 T /tmp/probe-merge' && "
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
                f"bash -c 'set -o pipefail; "
                f"( echo line; exit 7 ) 2>&1 | {s6log_in_sbx} -b n20 s1000000 T /tmp/probe-rc; "
                f"echo got=${{PIPESTATUS[0]}}'"
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
    finally:
        shutil.rmtree(ws, ignore_errors=True)
