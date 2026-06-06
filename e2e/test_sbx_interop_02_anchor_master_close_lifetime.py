"""interop: PTY-spawned anchor survives devm closing the master.

devm's anchor spawn (internal/orchestrator/shell.go: pty.StartWithSize
of bare `sbx run ...`) needs to outlive the PTY master closing,
because devm continues with port reconcile, user-shell spawn, and
eventual exit — the master will close at various points along the
way. If the anchor died at master-close, devm's whole anchor-alive
model would collapse and the sandbox would die with it.

Production shape pinned: Go's pty.StartWithSize spawn of bare sbx
run (no nohup wrapping). sbx ignores SIGHUP when running under a
controlling TTY (TUI-style signal handling). This is what enables
us to drop the historical nohup wrap.

Probe binary: e2e/probes/probe-anchor-lifetime/main.go.

If this goes red: either sbx changed its signal handling (it
stopped ignoring SIGHUP under TTY), creack/pty changed how the
slave is bound as controlling tty, or some other Go-primitive ↔
sbx interaction shifted. The fix is probably restoring the nohup
wrap in shell.go (and reverting this test).
"""
from __future__ import annotations

import pytest

from helpers.interop import build_probe, run_probe

pytestmark = pytest.mark.sbx_interop


@pytest.mark.timeout(120)
def test_anchor_outlives_master_close(sandbox_name):
    binpath = build_probe("probe-anchor-lifetime")
    r = run_probe(binpath, sandbox_name, timeout=90)
    assert r.returncode == 0, (
        f"probe exited {r.returncode}; expected 0 = anchor survived.\n"
        f"  1 = anchor PID dead after master close (the primitive broke)\n"
        f"  2 = anchor alive but sbx ls no longer shows running\n"
        f"  3 = sandbox bring-up failed\n"
        f"stdout={r.stdout.decode()!r}\nstderr={r.stderr.decode()!r}"
    )
