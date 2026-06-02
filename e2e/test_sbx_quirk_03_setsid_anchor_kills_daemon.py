"""sbx-quirk 03: OBSERVATION test for cross-session-anchor behavior.

During test_sbx_anchor_07's first run, a setsid'd anchor (`sbx run`
in its own session) combined with an interactive user shell in a
DIFFERENT session produced a daemon death at ~5s. That observation
was the basis for our "don't setsid the anchor" architectural rule.

When this guard test was added and run repeatedly, the death did
NOT reproduce — 4 of 5 runs the daemon survived past 20s with
setsid. So the cross-session kill is FLAKY rather than guaranteed.

The architectural rule remains useful as a precaution: same-session
anchor is observably more reliable than setsid'd anchor. But this
test no longer ASSERTS death; it RECORDS the observed outcome so
flakiness changes (e.g. moving toward consistent death, or toward
consistent survival) are visible across sbx upgrades.

If a future investigation pins down the exact conditions for the
~5s death, this file should be updated to assert it deterministically.
"""
from __future__ import annotations
import os
import re
import signal
import subprocess
import textwrap
import time

import pexpect
import pytest

from helpers import sbx
from helpers.sbx_kit import (
    materialize_kit,
    read_daemon_lifetime,
)


WRAPPER = textwrap.dedent("""\
    #!/usr/bin/env python3
    \"\"\"Spawn anchor with setsid (own session), wait ready, execvp
    into sbx exec -it bash.\"\"\"
    import os, subprocess, sys, time

    pidfile, logfile, kit_dir, name, agent, workspace = sys.argv[1:7]

    # setsid via start_new_session — anchor in its OWN session.
    with open(logfile, "ab") as logf:
        proc = subprocess.Popen(
            ["sbx", "run", "--kit", kit_dir, "--name", name, agent, workspace],
            stdin=subprocess.DEVNULL,
            stdout=logf,
            stderr=logf,
            start_new_session=True,
        )
    with open(pidfile, "w") as pf:
        pf.write(str(proc.pid))

    def state():
        try:
            r = subprocess.run(["sbx", "ls"], capture_output=True, timeout=10, text=True)
        except Exception:
            return None
        for line in r.stdout.splitlines()[1:]:
            fields = line.split()
            if fields and fields[0] == name and len(fields) >= 3:
                return fields[2]
        return None

    deadline = time.monotonic() + 60
    while time.monotonic() < deadline:
        if state() == "running": break
        time.sleep(0.25)
    else:
        sys.exit(2)
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        r = subprocess.run(["sbx", "exec", name, "true"], capture_output=True, timeout=5)
        if r.returncode == 0: break
        time.sleep(0.25)
    else:
        sys.exit(3)

    os.execvp("sbx", ["sbx", "exec", "-it", name, "bash"])
""")


def _pid_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False


@pytest.mark.timeout(180)
def test_setsid_anchor_with_interactive_user_shell_observation(sandbox_name, tmp_path):
    kit = materialize_kit()
    wrapper = tmp_path / "wrapper.py"
    wrapper.write_text(WRAPPER)
    wrapper.chmod(0o755)
    pidfile = tmp_path / "anchor.pid"
    logfile = tmp_path / "anchor.log"

    anchor_pid = None
    child = None
    try:
        child = pexpect.spawn(
            "python3",
            [str(wrapper), str(pidfile), str(logfile), kit.kit_dir,
             sandbox_name, "anchortest", kit.workspace],
            encoding="utf-8",
            timeout=90,
            dimensions=(40, 200),
        )
        child.expect(re.compile(r"[#$] $|[#$] "), timeout=90)

        assert pidfile.exists()
        anchor_pid = int(pidfile.read_text().strip())
        assert _pid_alive(anchor_pid)

        # Wait past the 5s window.
        time.sleep(20)

        # Anchor is in its own session and we never killed it; the
        # process is still alive.
        assert _pid_alive(anchor_pid), (
            "anchor died unexpectedly — test is testing the wrong thing"
        )

        # But the daemon is dead — that's the cross-session quirk.
        result = read_daemon_lifetime(sandbox_name)
        assert result is not None, "could not read daemon trail"
        start, last, count, alive = result
        lifetime = last - start
        print(f"\n  start={start:.3f} last={last:.3f} count={count} "
              f"alive={alive} lifetime={lifetime:.2f}s\n", flush=True)

        # Record only — see module docstring. The test passes as long
        # as we successfully read the daemon trail; the outcome
        # (alive/dead) is logged for trend tracking.
        print(f"  observation: setsid_anchor_alive=True "
              f"daemon_alive={alive} lifetime={lifetime:.2f}s",
              flush=True)
    finally:
        try:
            if child is not None and child.isalive():
                child.close(force=True)
        except Exception:
            pass
        if anchor_pid is not None and _pid_alive(anchor_pid):
            try:
                os.kill(anchor_pid, signal.SIGKILL)
            except Exception:
                pass
        sbx.sandbox_rm(sandbox_name)
        kit.cleanup()
