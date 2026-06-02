"""sbx-anchor 08: `setpgid` (own process group, SAME session as user
shell) is COMPATIBLE with daemon survival under an interactive user
shell — unlike `setsid` which kills the daemon at 5s.

The architecture-required claim: when we eventually want to insulate
the anchor from terminal-close SIGHUP cascades, we should reach for
`setpgid` (own PG, same session) rather than `setsid` (own session)
because the latter triggers the 5s daemon kill.

This test verifies that compatibility. It does NOT verify that a
real terminal-close keeps the sandbox alive — that's a separate
end-to-end concern about devm's lifecycle when the user closes their
terminal app without typing `exit` first. See HANDOFF.md for the
follow-up. Whatever the answer there, the lower bar tested here
(setpgid is at least compatible with the daemon) must pass first.
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
    \"\"\"Wrapper: fork anchor with setpgid (own PG, same session),
    wait ready, execvp into sbx exec -it bash.\"\"\"
    import os, subprocess, sys, time

    pidfile, logfile, kit_dir, name, agent, workspace = sys.argv[1:7]

    def setpgid_only():
        # New process group; session unchanged.
        os.setpgid(0, 0)

    with open(logfile, "ab") as logf:
        proc = subprocess.Popen(
            ["sbx", "run", "--kit", kit_dir, "--name", name, agent, workspace],
            stdin=subprocess.DEVNULL,
            stdout=logf,
            stderr=logf,
            preexec_fn=setpgid_only,
        )
    with open(pidfile, "w") as pf:
        pf.write(str(proc.pid))

    # Wait running.
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
def test_setpgid_anchor_compatible_with_daemon(sandbox_name, tmp_path):
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

        assert pidfile.exists(), "wrapper didn't write anchor pid"
        anchor_pid = int(pidfile.read_text().strip())
        assert _pid_alive(anchor_pid), "anchor not alive after wrapper exec'd"

        # Daemon must survive under setpgid anchor + interactive
        # user shell. If setpgid triggers the 5s kill (like setsid
        # does), this assertion fires and setpgid is also not viable.
        time.sleep(20)
        assert _pid_alive(anchor_pid), "anchor died during shell session"
        result = read_daemon_lifetime(sandbox_name)
        assert result is not None
        start, last, count, alive = result
        lifetime = last - start
        print(f"\n  anchor_pid={anchor_pid} alive={alive} "
              f"lifetime={lifetime:.2f}s\n", flush=True)
        assert alive and lifetime > 15, (
            f"setpgid anchor + interactive shell killed the daemon "
            f"(alive={alive}, lifetime={lifetime:.2f}s). setpgid is "
            f"not compatible with daemon survival — needs different "
            f"approach if we ever want to insulate the anchor from "
            f"terminal-close cascades."
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
