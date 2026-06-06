"""interop: Go exec.Command + `sbx ls --json` round-trips parseable JSON.

The lowest-level Go ↔ sbx primitive devm relies on: spawn sbx via
Go's os/exec, capture its stdout, get back the structured shape
sbx promises. Every devm-side sbx call is built on top of this one.

What this catches if red: Go's `exec.Command` ↔ sbx layer broke at
the syscall, capture, or encoding level — independent of any
sandbox state, PTY/anchor concerns, or higher-level orchestration.

What this deliberately does NOT cover (other interop tests do):
  - Anchor/PTY behavior under long-lived sbx run
  - Port reconciliation
  - exec.Cmd stdin shapes (pipe, fd-passing)

The probe binary lives at e2e/probes/probe-exec-basic/main.go.
"""
from __future__ import annotations

import re

import pytest

from helpers.interop import build_probe, run_probe

pytestmark = pytest.mark.sbx_interop

# Probe stdout shape:
#   "OK\tsandboxes=N\tfirst_keys=[k1 k2 ...]\n"   (when N > 0)
#   "OK\tsandboxes=0\tfirst_keys=[]\n"            (when N == 0)
# We assert the prefix + a numeric count + the keys bracket. The
# specific count depends on running sandboxes — irrelevant to the
# interop question; we pin that Go received structured output it
# could parse.
STDOUT_SHAPE = re.compile(rb"^OK\tsandboxes=(\d+)\tfirst_keys=\[(.*)\]\n$")


@pytest.mark.timeout(30)
def test_exec_command_sbx_ls_json_roundtrips():
    binpath = build_probe("probe-exec-basic")
    r = run_probe(binpath, timeout=10)
    assert r.returncode == 0, (
        f"probe exited {r.returncode} (expected 0 = exec + parse OK); "
        f"stderr={r.stderr.decode()!r}"
    )
    m = STDOUT_SHAPE.match(r.stdout)
    assert m, f"unexpected stdout shape: {r.stdout!r}"
