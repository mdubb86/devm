"""sbx-anchor 07: end-to-end production-shape simulation.

pexpect spawns a wrapper Python script. The wrapper:
  1. Forks the anchor (`sbx run`, stdio to a logfile, stdin=DEVNULL,
     INHERITS the wrapper's session — no setsid)
  2. Waits for the sandbox to reach running + exec-ready
  3. `os.execvp`s into `sbx exec -it <name> bash`

LOAD-BEARING FINDING (cost: 1 failed run of this test):
  An anchor placed in its OWN session via `setsid` + an interactive
  user shell makes sbx kill startup-launched daemons at the same
  ~5s mark we already documented for anchor-death. With the anchor
  in the SAME session as the user shell, the daemon survives.

  In production, devm spawns the anchor as a child of itself (no
  setsid), so the anchor inherits the user-terminal session that
  the user shell also lives in. That's the right shape.

  See docs/sbx-quirks.md for the full table of when daemons die
  vs. survive.

Because the wrapper REPLACES itself with `sbx exec -it bash`
(`execvp`), the user-shell process inherits the wrapper's stdio — and
the wrapper's stdio is pexpect's PTY. That puts the user shell in the
same shared-PTY-via-inheritance shape that real users get when devm's
Go `exec.Cmd` inherits the host terminal's stdio.

Pexpect's role here is exactly what we agreed: drive the bash session
as a user would. It does NOT spawn `sbx exec` directly (which would
allocate a fresh PTY and mask the production shape).

This test asserts:
  - The user shell shows a prompt (sandbox really is up + exec works).
  - We can type commands and see output.
  - We can `exit` and the user shell terminates cleanly.
  - After exit, the anchor is still alive and the sandbox is still
    running (orphaned anchor outlives the user session).
  - The background daemon is alive throughout — i.e. the
    production-shape user shell never triggers the 5s kill, because
    the anchor never dies.
"""
from __future__ import annotations
import os
import re
import signal
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
    \"\"\"Production-shape wrapper. Forks anchor; execvps into sbx exec -it bash.\"\"\"
    import os, subprocess, sys, time

    pidfile, logfile, kit_dir, name, agent, workspace = sys.argv[1:7]

    # PROBE: anchor in SAME session as wrapper (no setsid). The wrapper
    # will execvp into sbx exec -it bash, which inherits the wrapper's
    # session — so anchor and user shell end up in the same session.
    with open(logfile, "ab") as logf:
        proc = subprocess.Popen(
            ["sbx", "run", "--kit", kit_dir, "--name", name, agent, workspace],
            stdin=subprocess.DEVNULL,
            stdout=logf,
            stderr=logf,
        )
    with open(pidfile, "w") as pf:
        pf.write(str(proc.pid))

    # Wait running + exec-ready (read sbx ls output until status is running).
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
        print("WRAPPER: never reached running", flush=True)
        sys.exit(2)

    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        r = subprocess.run(["sbx", "exec", name, "true"], capture_output=True, timeout=5)
        if r.returncode == 0: break
        time.sleep(0.25)
    else:
        print("WRAPPER: never exec-ready", flush=True)
        sys.exit(3)

    # Replace ourselves with the user shell. The execvp'd process
    # inherits our stdin/stdout/stderr — which is pexpect's PTY.
    os.execvp("sbx", ["sbx", "exec", "-it", name, "bash"])
""")


def _pid_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False


@pytest.mark.timeout(180)
def test_wrapper_inherited_user_shell_keeps_daemon(sandbox_name, tmp_path):
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

        # The wrapper takes a few seconds to bring up the sandbox before
        # exec'ing into bash. After exec, bash prints a prompt to our
        # PTY. Match any of the common bash prompt patterns.
        child.expect(re.compile(r"[#$] $|[#$] "), timeout=90)

        # Anchor PID should have been written before exec.
        assert pidfile.exists(), "wrapper didn't write anchor pid"
        anchor_pid = int(pidfile.read_text().strip())
        assert _pid_alive(anchor_pid), "anchor not alive after wrapper exec'd"

        # Type a marker command — verify the shell really is interactive.
        child.sendline("echo READY_$(whoami)")
        child.expect(r"READY_agent", timeout=10)

        # Let the daemon log for a while (well past 5s).
        time.sleep(20)

        # Daemon is still alive (anchor still holding the session).
        assert _pid_alive(anchor_pid), "anchor died during shell session"
        result = read_daemon_lifetime(sandbox_name)
        assert result is not None
        start, last, count, alive = result
        lifetime = last - start
        print(f"\n  start={start:.3f} last={last:.3f} count={count} "
              f"alive={alive} lifetime={lifetime:.2f}s\n", flush=True)
        assert alive, "daemon died while wrapper-driven shell was up"
        assert lifetime > 15

        # Exit the user shell.
        child.sendline("exit")
        child.expect(pexpect.EOF, timeout=15)

        # Anchor should STILL be alive (orphaned, not tied to the
        # user shell). Sandbox still running.
        assert _pid_alive(anchor_pid), (
            "anchor died when the user shell exited — orphaning isn't "
            "working: the anchor was somehow tied to the shell"
        )
        assert sbx.sandbox_state(sandbox_name) == "running", (
            "sandbox stopped when the user shell exited; anchor is "
            "not holding the session on its own"
        )
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
