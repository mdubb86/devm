"""134: `devm status` reflects iron-proxy death (adopted-supervisor path).

The scenario that fooled `devm status` on buzztrack (2026-07-31):
iron-proxy was adopted post-daemon-restart, then died. `devm status`
kept reporting `iron-proxy: ok` because `Supervisor.Status()` for
adopted entries only checks `syscall.Kill(pid, 0)` — it doesn't verify
the process at that PID is still iron-proxy.

This test exercises the adopted path (via `devm service restart`)
because that's where the observed bug lives. Pexec-managed variant
isn't tested here — pexec respawns iron-proxy in <500ms after death,
so any window where status could lie is tiny and racy to catch.

Sequence: cold-start → devm service restart (iron-proxy now adopted) →
SIGKILL both shim + child → poll devm status every 200ms for 5s →
assert never lies (status=ok while pgrep confirms iron-proxy dead) and
eventually reports MISSING.

Caveat about coverage: the observed bug is PID reuse — after iron-proxy
dies, macOS recycles the PID to some other process and kill(pid, 0)
succeeds for the wrong process. This test does NOT force reuse
(deterministic PID reassignment is impossible on macOS), so on machines
without natural churn during the 5-second window, the test passes even
if Bug A is live. What this test DOES reliably catch: any regression
where sup.Status returns Present=true for an adopted PID that returned
ESRCH from kill(0), or where the daemon fails to remove the adopted
entry after Stop, or a similar code-path bug that manifests without
requiring PID reuse. Consider this a code-path pin, not a repro.
"""
from __future__ import annotations

import os
import signal
import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


def _iron_proxy_pids_for(project_id: str) -> list[int]:
    """Return all PIDs (shim + child) matching this project's iron-proxy."""
    r = subprocess.run(
        ["ps", "-axo", "pid=,command="],
        capture_output=True, text=True, check=True,
    )
    needle = f"/iron-proxy/{project_id}.yaml"
    pids: list[int] = []
    for line in r.stdout.splitlines():
        if needle in line:
            pids.append(int(line.strip().split(None, 1)[0]))
    return pids


def _iron_proxy_child_pid(project_id: str) -> int | None:
    """PID of the iron-proxy child (not the shim). Same as test_44's helper
    but explicit about skipping the shim entry."""
    r = subprocess.run(
        ["ps", "-axo", "pid=,command="],
        capture_output=True, text=True, check=True,
    )
    needle = f"/iron-proxy/{project_id}.yaml"
    for line in r.stdout.splitlines():
        if needle in line and "devm-setsid-shim" not in line:
            return int(line.strip().split(None, 1)[0])
    return None


def _status_iron_proxy_line(devm_path: str, workspace_path: str) -> str:
    """Run `devm status` in the project workspace, return the iron-proxy line.

    `devm status` uses non-zero exit codes as semantic signals (e.g. rc=4
    for a MISSING iron-proxy is expected and CORRECT — the whole point of
    this test is to observe that state). Parse the output regardless of
    exit code. Missing iron-proxy line in output signals daemon
    unreachability — the caller's polling logic surfaces that.
    """
    r = subprocess.run(
        [devm_path, "status"],
        cwd=workspace_path, capture_output=True, timeout=30, check=False,
    )
    for line in r.stdout.decode().splitlines():
        if "iron-proxy:" in line:
            return line.strip()
    return ""


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_devm_status_reflects_adopted_iron_proxy_death(
    devm, workspace, sandbox_name, devm_installed,
):
    workspace.write_devmyaml(
        install=["true"],
        services={"sleep": {"exec": ["/bin/sleep", "infinity"], "restart": "always"}},
        network={"allow": ["httpbin.org"]},
    )

    try:
        # 1. Cold-start.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        pid_before = _iron_proxy_child_pid(workspace.slug)
        assert pid_before is not None, "iron-proxy should be running after cold-start"

        # 2. Service restart — iron-proxy now adopted.
        r = subprocess.run(
            [devm.path, "service", "restart"],
            capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"service restart failed:\n{r.stderr.decode()}"
        time.sleep(2)  # Adoption settle window, same as test_44.
        pid_after_restart = _iron_proxy_child_pid(workspace.slug)
        assert pid_after_restart == pid_before, (
            f"iron-proxy PID changed across service restart: "
            f"{pid_before} → {pid_after_restart}. Adoption failed; "
            f"cannot exercise adopted-supervisor status path."
        )

        # 3. Kill iron-proxy AND its shim. Adopted iron-proxy has no
        # pexec auto-restart; killing both ensures no one respawns it
        # during the observation window.
        pids_to_kill = _iron_proxy_pids_for(workspace.slug)
        assert len(pids_to_kill) >= 2, (
            f"expected both shim + iron-proxy PIDs, got {pids_to_kill}"
        )
        for pid in pids_to_kill:
            try:
                os.kill(pid, signal.SIGKILL)
            except ProcessLookupError:
                pass  # already gone

        # Settle: wait for kernel to actually reap the killed processes so ps
        # no longer shows them (avoids a race where the first poll iteration
        # still sees zombies and prematurely triggers the "respawned" branch).
        settle_deadline = time.monotonic() + 3.0
        while time.monotonic() < settle_deadline:
            if not _iron_proxy_pids_for(workspace.slug):
                break
            time.sleep(0.1)

        # 4. Poll for 5 seconds. Assert status eventually reports MISSING
        # (STALE is unreachable here — it requires Present+Running with a
        # version mismatch, not a dead process) WHILE pgrep confirms
        # iron-proxy is actually dead. A single observation of "ok"
        # while pgrep is empty is the bug.
        deadline = time.monotonic() + 5.0
        saw_missing = False
        saw_lie = False
        first_lie_snapshot = ""
        while time.monotonic() < deadline:
            alive_pids = _iron_proxy_pids_for(workspace.slug)
            status_line = _status_iron_proxy_line(devm.path, str(workspace.path))

            if not alive_pids and "iron-proxy: ok" in status_line:
                # LIE: no iron-proxy alive, but status says ok. Under this
                # test's harness, this can only fire via naturally-occurring
                # PID reuse (see module docstring's coverage caveat) — we
                # don't force reuse, so a pass here doesn't rule out Bug A.
                saw_lie = True
                if not first_lie_snapshot:
                    first_lie_snapshot = (
                        f"pgrep=empty, status={status_line!r}"
                    )
            if not alive_pids and "MISSING" in status_line:
                saw_missing = True
                break
            if alive_pids:
                # Something respawned iron-proxy (shouldn't happen for
                # adopted, but be robust to unexpected behavior).
                break

            time.sleep(0.2)

        assert not saw_lie, (
            f"devm status LIED: reported `iron-proxy: ok` while iron-proxy was "
            f"actually dead (pgrep found nothing).\n"
            f"First observed lie: {first_lie_snapshot}\n"
            f"This is the buzztrack Bug A (Supervisor.Status returns "
            f"Present=true for adopted PIDs based on kill(pid, 0) alone, "
            f"without verifying the process is still iron-proxy)."
        )
        # If we exited the loop without ever seeing MISSING, the loop
        # broke because iron-proxy came back alive (respawned somehow).
        # That's not the bug we're testing for, but we should still
        # note it.
        if not saw_missing:
            final_alive = _iron_proxy_pids_for(workspace.slug)
            assert final_alive, (
                f"iron-proxy neither showed as MISSING nor respawned within 5s. "
                f"Final pgrep: empty. Final status: "
                f"{_status_iron_proxy_line(devm.path, str(workspace.path))!r}"
            )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
