"""140: iron-proxy survives an ungraceful daemon death.

Reproduces the everstone Bug B post-mortem: on 2026-08-14 a user
`devm install` triggered a daemon SIGTERM. Between the daemon
receiving SIGTERM and fully exiting, iron-proxy was mid-write to its
inherited stdout (a pexec-owned pipe). The pipe read-end lived in the
daemon; when the daemon exited, the read-end closed. Iron-proxy's next
write raised EPIPE; Go's runtime terminates on SIGPIPE for fd 1 or fd
2. Iron-proxy died despite the setsid detach.

test_44 (`devm service restart`) and test_133 (`devm install`) both
run against an iron-proxy that is IDLE — a minimal allowlist and no
active traffic — so the pipe closure never coincides with a write and
the SIGPIPE cascade never fires. Neither test catches this failure.
This test forces the race by:
  1. Cold-starting with an iron-proxy that has real allowlist entries.
  2. Driving continuous requests through iron-proxy from the guest
     while the daemon is killed — iron-proxy audits every request to
     its stdout log, guaranteeing a write is in flight when the pipe
     closes.
  3. SIGKILLing the daemon (not `devm service restart`) — instant
     exit, no graceful cleanup, pipe is closed the moment the daemon
     process ends. Fastest possible reproduction.
  4. Asserting iron-proxy's PID is unchanged after the daemon dies.

Post-fix: the shim owns a fresh pipe between itself and iron-proxy
and absorbs writes on the outer pipe silently. Iron-proxy stays
alive; the audit log's tail is lost (expected — daemon is gone).

Pairs with the Go unit test cmd/devm-setsid-shim:
TestShim_ChildSurvivesParentStdoutClose, which pins the same
invariant deterministically without the sudo cost of a real daemon.
"""
from __future__ import annotations

import os
import signal
import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


def _pid_of_iron_proxy_for(project_id: str) -> int | None:
    """Iron-proxy child PID (not the shim) for this project, or None."""
    r = subprocess.run(
        ["ps", "-axo", "pid=,command="],
        capture_output=True, text=True, check=True,
    )
    needle = f"/iron-proxy/{project_id}.yaml"
    for line in r.stdout.splitlines():
        if needle in line and "devm-setsid-shim" not in line:
            return int(line.strip().split(None, 1)[0])
    return None


def _pid_of_daemon(devm_path: str) -> int | None:
    """Return the daemon's PID (`<devm_path> serve`), or None if not up."""
    r = subprocess.run(
        ["pgrep", "-f", f"{devm_path} serve"],
        capture_output=True, text=True,
    )
    if r.returncode != 0:
        return None
    lines = r.stdout.strip().split()
    return int(lines[0]) if lines else None


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_iron_proxy_survives_daemon_sigkill(
    devm, workspace, sandbox_name, devm_installed,
):
    workspace.write_devmyaml(
        install=["true"],
        services={"sleep": {"exec": ["/bin/sleep", "infinity"], "restart": "always"}},
        # Real allowlist so iron-proxy writes an audit line for every
        # request the traffic generator makes.
        network={"allow": ["httpbin.org"]},
    )

    traffic = None
    try:
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        pid_before = _pid_of_iron_proxy_for(workspace.slug)
        assert pid_before is not None, (
            f"iron-proxy should be running for {workspace.slug!r} after cold-start"
        )

        daemon_pid = _pid_of_daemon(devm.path)
        assert daemon_pid is not None, "daemon PID not found via pgrep"

        # Traffic generator: loop `curl` from the guest until we kill
        # the daemon. Each request forces iron-proxy to write an audit
        # line to its stdout — pexec's pipe is being actively written
        # to when we sever the read-end.
        traffic = subprocess.Popen(
            [devm.path, "exec", "--",
             "sh", "-c",
             "while true; do curl -sSf -o /dev/null https://httpbin.org/status/200 || true; done"],
            cwd=str(workspace.path),
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )

        # Give the traffic a moment to actually hit iron-proxy.
        time.sleep(1.0)

        # SIGKILL the daemon — no graceful cleanup, instant pipe close.
        # This is more aggressive than any user-initiated shutdown, so
        # if iron-proxy survives this it survives everything.
        os.kill(daemon_pid, signal.SIGKILL)

        # Wait for the daemon process to actually be reaped.
        for _ in range(20):
            try:
                os.kill(daemon_pid, 0)
            except ProcessLookupError:
                break
            time.sleep(0.1)
        else:
            pytest.fail(f"daemon (pid {daemon_pid}) still alive 2s after SIGKILL")

        # Give the SIGPIPE cascade a full second to fire if it's going
        # to. Pre-fix, iron-proxy dies within ~10ms of the daemon's
        # pipe close.
        time.sleep(1.0)

        # Iron-proxy MUST still be running with the same PID. Post-fix
        # the shim absorbs the broken-pipe write; pre-fix iron-proxy
        # is gone.
        pid_after = _pid_of_iron_proxy_for(workspace.slug)
        if pid_after is None:
            raise AssertionError(
                f"iron-proxy is GONE after daemon SIGKILL: "
                f"pid_before={pid_before}, pid_after=None. "
                f"The shim's inherited stdout pipe closed with the "
                f"daemon, iron-proxy hit SIGPIPE on its next write, "
                f"and Go's default handler killed it. See "
                f"cmd/devm-setsid-shim: teeAbsorb + signal.Ignore("
                f"SIGPIPE)."
            )
        if pid_after != pid_before:
            raise AssertionError(
                f"iron-proxy RESPAWNED across daemon SIGKILL: "
                f"pid_before={pid_before}, pid_after={pid_after}. "
                f"Original process died and something spawned a new "
                f"one (watchdog?) — protection failed."
            )
    finally:
        if traffic is not None:
            traffic.kill()
            try:
                traffic.wait(timeout=5)
            except subprocess.TimeoutExpired:
                pass
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
