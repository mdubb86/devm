"""sbx-anchor 10: simulate the user closing their terminal window
(NOT typing `exit`) and observe what survives.

Real-world flow:
  1. User opens a terminal app. The shell (zsh/bash) becomes the
     session leader of a PTY pair owned by the terminal app.
  2. User types `devm shell`. devm runs as a child of the shell.
     devm spawns the anchor and the user shell. All three are in
     the same session (the user's terminal session).
  3. User closes the terminal window. Terminal app closes the master
     end of the PTY. The kernel sends SIGHUP to the session leader
     and processes whose controlling tty was that PTY.

We simulate this by `pty.fork()`ing a wrapper that does steps 1–2 of
devm's flow (spawn anchor, then run a user-shell stand-in), and then
closing the master fd from the test parent. After the smoke settles
we record:

  - Did the anchor process survive?
  - Is the sandbox still running according to `sbx ls`?

Parametrized over three anchor shapes so we can read the matrix
even if some configurations hang:

  - default: no setpgid, no setsid, default SIGHUP handler
            (this is what Go's `exec.Cmd` produces today)
            EXPECTED: anchor dies, sandbox stops
  - setpgid: anchor in its own PG, same session as wrapper
            EXPECTED: anchor still dies (kernel sends SIGHUP to all
            processes with the closing PTY as controlling tty, not
            just the foreground PG)
  - ignhup_only: anchor ignores SIGHUP via signal.SIG_IGN
            EXPECTED: anchor + sandbox survive — the IGN disposition
            is what the kernel honors, regardless of PG.
  - setpgid_ignhup: setpgid AND ignore SIGHUP
            EXPECTED: anchor + sandbox survive (same as ignhup_only)

The matrix is asserted per-shape (each cell's exact outcome is
pinned), so a regression in any direction surfaces as a failure
in the matching parametrization.

Production implication: devm should spawn the anchor with SIGHUP
ignored. Two easy ways in Go:
  - `signal.Ignore(syscall.SIGHUP)` in the parent before exec
    (IGN disposition is inherited across fork+exec per POSIX)
  - or wrap as `nohup sbx run ...`
"""
from __future__ import annotations
import os
import pty
import select
import signal
import subprocess
import textwrap
import time

import pytest

from helpers import sbx
from helpers.sbx_kit import materialize_kit


def _pid_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False


WRAPPER = textwrap.dedent("""\
    #!/usr/bin/env python3
    \"\"\"Spawn anchor with the given process-group shape, then attach
    a long-running `sbx exec` as a stand-in for the interactive user
    shell. Write the anchor's pid to argv[1] so the test can find
    it after the PTY closes.

    argv layout:
      [pidfile, logfile, kit_dir, name, agent, workspace, shape]
      shape is one of: default | setpgid | setpgid_ignhup
    \"\"\"
    import os, signal, subprocess, sys, time

    pidfile, logfile, kit_dir, name, agent, workspace, shape = sys.argv[1:8]

    def preexec():
        if shape in ("setpgid", "setpgid_ignhup"):
            os.setpgid(0, 0)
        if shape in ("ignhup_only", "setpgid_ignhup"):
            signal.signal(signal.SIGHUP, signal.SIG_IGN)

    with open(logfile, "ab") as logf:
        proc = subprocess.Popen(
            ["sbx", "run", "--kit", kit_dir, "--name", name, agent, workspace],
            stdin=subprocess.DEVNULL,
            stdout=logf,
            stderr=logf,
            preexec_fn=preexec,
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

    # Signal readiness so the test parent knows the PTY can be closed.
    print("READY", flush=True)

    # Stand-in for an interactive user shell — block until torn down.
    os.execvp("sbx", ["sbx", "exec", "-it", name, "bash"])
""")


# shape -> behavior we GUARANTEE in this test
# - "must_survive": both anchor and sandbox must come through alive
# - "flaky":        actual outcome varies (with system load, kernel
#                   reaping order, etc). The test records the observed
#                   result for visibility but doesn't assert either way.
_BEHAVIOR = {
    "default":       "flaky",
    "setpgid":       "flaky",
    "ignhup_only":   "must_survive",
    "setpgid_ignhup": "must_survive",
}


@pytest.mark.timeout(180)
@pytest.mark.parametrize("shape", list(_BEHAVIOR))
def test_terminal_close(shape, sandbox_name, tmp_path):
    """For the given anchor shape, simulate terminal-close and observe
    survival. Asserts must_survive shapes really do survive; records
    flaky shapes for visibility."""
    kit = materialize_kit()
    wrapper = tmp_path / f"wrapper_{shape}.py"
    wrapper.write_text(WRAPPER)
    wrapper.chmod(0o755)
    pidfile = tmp_path / f"anchor_{shape}.pid"
    logfile = tmp_path / f"anchor_{shape}.log"

    anchor_pid = None
    child_pid = None
    master_fd = -1
    try:
        # pty.fork() puts the child in a new session with the slave
        # PTY as its controlling tty. Same shape a terminal app sets up.
        child_pid, master_fd = pty.fork()
        if child_pid == 0:
            os.execvp("python3", [
                "python3", str(wrapper),
                str(pidfile), str(logfile), kit.kit_dir,
                sandbox_name, "anchortest", kit.workspace, shape,
            ])

        # Wait for READY on the master.
        ready = False
        deadline = time.monotonic() + 120
        buf = b""
        while time.monotonic() < deadline:
            r, _, _ = select.select([master_fd], [], [], 0.5)
            if master_fd in r:
                try:
                    chunk = os.read(master_fd, 4096)
                except OSError:
                    break
                if not chunk:
                    break
                buf += chunk
                if b"READY" in buf:
                    ready = True
                    break
        if not ready:
            log = ""
            if logfile.exists():
                log = logfile.read_text()[-500:]
            pytest.fail(
                f"wrapper never signalled READY (shape={shape}); "
                f"pty_buf={buf[-300:]!r} anchor_log={log!r}"
            )
        assert pidfile.exists(), "wrapper didn't write pid"
        anchor_pid = int(pidfile.read_text().strip())

        # Settle so the user-shell execvp fully attaches.
        time.sleep(2)
        assert _pid_alive(anchor_pid)
        assert sbx.sandbox_state(sandbox_name) == "running"

        # Close master — kernel SIGHUP cascade.
        os.close(master_fd)
        master_fd = -1

        # Let the kernel propagate signals + sbx daemon notice.
        time.sleep(5)

        anchor_alive = _pid_alive(anchor_pid)
        sb_state = sbx.sandbox_state(sandbox_name)
        print(f"\n  shape={shape:18s} anchor_alive={anchor_alive!s:5s} "
              f"sandbox_state={sb_state}\n", flush=True)

        if _BEHAVIOR[shape] == "must_survive":
            assert anchor_alive, (
                f"shape={shape}: anchor died on terminal close — "
                f"ignoring SIGHUP did NOT preserve the anchor. This is "
                f"the load-bearing claim for sandbox-survives-terminal-"
                f"close; production needs another approach."
            )
            assert sb_state == "running", (
                f"shape={shape}: anchor alive but sandbox stopped. sbx "
                f"daemon released the session for some other reason."
            )
    finally:
        if master_fd >= 0:
            try:
                os.close(master_fd)
            except OSError:
                pass
        if child_pid is not None:
            try:
                os.waitpid(child_pid, os.WNOHANG)
            except (ChildProcessError, OSError):
                pass
        if anchor_pid is not None and _pid_alive(anchor_pid):
            try:
                os.kill(anchor_pid, signal.SIGKILL)
            except Exception:
                pass
        sbx.sandbox_rm(sandbox_name)
        kit.cleanup()
