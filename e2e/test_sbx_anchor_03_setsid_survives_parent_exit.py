"""sbx-anchor 03: an `sbx run` anchor launched with `setsid` and
stdio redirected to a logfile survives its parent process exiting.

This is the second load-bearing assumption of the new architecture:
devm's `devm shell` cold-start spawns the anchor and then itself
exits when the user shell exits. The anchor must outlive devm
without being SIGHUPed or otherwise reaped.

The test uses a one-shot helper script that:
  1. Spawns `sbx run` with `os.setsid()` in the child, stdio
     redirected to a log file under the workspace.
  2. Waits for the sandbox to reach running + exec-ready.
  3. Exits with rc=0.

After the helper script exits, we verify the anchor PID is still
alive, the sandbox is still running, and the daemon is still alive
30 seconds later.
"""
from __future__ import annotations
import os
import signal
import subprocess
import textwrap
import time

import pytest

from helpers import sbx
from helpers.sbx_kit import (
    materialize_kit,
    read_daemon_lifetime,
    wait_exec_ready,
    wait_running,
)


SPAWNER_SCRIPT = textwrap.dedent("""\
    #!/usr/bin/env python3
    \"\"\"Spawn sbx run in its own session, write its pid to argv[1], exit.\"\"\"
    import os, subprocess, sys
    pidfile, kit_dir, name, agent, workspace, logfile = sys.argv[1:7]
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
    # The Popen handle is intentionally NOT waited on. The child stays
    # alive after this parent exits.
""")


def _pid_alive(pid: int) -> bool:
    try:
        os.kill(pid, 0)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        return True


@pytest.mark.timeout(120)
def test_setsid_anchor_survives_parent_exit(sandbox_name, tmp_path):
    kit = materialize_kit()
    spawner = tmp_path / "spawner.py"
    spawner.write_text(SPAWNER_SCRIPT)
    spawner.chmod(0o755)
    pidfile = tmp_path / "anchor.pid"
    logfile = tmp_path / "anchor.log"

    anchor_pid = None
    try:
        # Run the spawner; wait for it to exit. The anchor it forks is
        # in a new session and should survive past this rc=0 return.
        result = subprocess.run(
            ["python3", str(spawner), str(pidfile), kit.kit_dir,
             sandbox_name, "anchortest", kit.workspace, str(logfile)],
            timeout=10,
            capture_output=True,
        )
        assert result.returncode == 0, (
            f"spawner exited rc={result.returncode}; "
            f"stdout={result.stdout!r} stderr={result.stderr!r}"
        )
        assert pidfile.exists(), "spawner didn't write the pidfile"
        anchor_pid = int(pidfile.read_text().strip())

        # Parent (the spawner) has exited. Anchor must still be alive.
        assert _pid_alive(anchor_pid), (
            f"anchor pid {anchor_pid} died with its parent — setsid did "
            f"not protect it from process-group cleanup. log: "
            f"{logfile.read_text() if logfile.exists() else '(no log)'}"
        )

        # Bring-up usually completes within a few seconds; allow up to 60.
        wait_running(sandbox_name)
        wait_exec_ready(sandbox_name)

        # Now let the daemon run for 30s and confirm it's alive — i.e.
        # the orphaned anchor really is holding the sandbox session.
        time.sleep(30)

        assert _pid_alive(anchor_pid), (
            f"anchor pid {anchor_pid} died during the 30s observation"
        )
        assert sbx.sandbox_state(sandbox_name) == "running", (
            "sandbox stopped while orphaned anchor was alive"
        )

        result = read_daemon_lifetime(sandbox_name)
        assert result is not None, "could not read daemon trail files"
        start, last, count, alive = result
        lifetime = last - start
        print(f"\n  anchor_pid={anchor_pid} start={start:.3f} "
              f"last={last:.3f} count={count} alive={alive} "
              f"lifetime={lifetime:.2f}s\n", flush=True)
        assert alive and lifetime > 25, (
            f"daemon not healthy under orphaned anchor: alive={alive} "
            f"lifetime={lifetime:.2f}s"
        )
    finally:
        if anchor_pid is not None and _pid_alive(anchor_pid):
            try:
                os.kill(anchor_pid, signal.SIGKILL)
            except Exception:
                pass
        sbx.sandbox_rm(sandbox_name)
        kit.cleanup()
