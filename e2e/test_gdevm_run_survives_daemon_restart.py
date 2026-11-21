"""`gdevm run -d <name>` survives a `systemctl restart gdevm-serve`
in the guest. After restart the new daemon rediscovers the orphan
and both the happy stop path (`gdevm stop <name>`) and the kill
path (external SIGKILL on the pgroup) are observable through
`gdevm runs`.

Pins the rediscovery contract end-to-end against a real guest:
state file on disk, KillMode=process keeping the child alive across
restart, Kill(pgid, 0) detection by the poller.
"""
from __future__ import annotations

import re
import subprocess
import time

import pytest

from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm


def _ggexec(devm_path, workspace_path, argv, *, timeout=30, check=False):
    """`devm exec <argv>` with the standard tart-transport retry wrapper."""
    return devm_exec_with_retry(
        devm_path, argv, cwd=str(workspace_path), timeout=timeout,
    )


def _wait_for_serve_active(devm_path, workspace_path, deadline_s=20):
    """Poll `systemctl is-active gdevm-serve.service` until it returns
    `active` or the deadline passes. Returns the final status string."""
    deadline = time.time() + deadline_s
    while time.time() < deadline:
        r = _ggexec(
            devm_path, workspace_path,
            ["systemctl", "is-active", "gdevm-serve.service"],
            timeout=10,
        )
        status = r.stdout.decode().strip()
        if status == "active":
            return status
        time.sleep(0.5)
    return status


def _parse_runs_pid(runs_stdout: str, name: str) -> int | None:
    """Pull the PID column out of `gdevm runs` output for a given name.
    Columns are: NAME PID STARTED MODE STATUS. Whitespace-separated
    (tabwriter emits spaces). Returns None if name not present."""
    for line in runs_stdout.splitlines():
        if not line or line.startswith("NAME"):
            continue
        fields = line.split()
        if fields and fields[0] == name and len(fields) >= 2:
            try:
                return int(fields[1])
            except ValueError:
                return None
    return None


def _cold_start_with_sleeper(devm, workspace, extra_commands=()):
    """Shared setup: devm.yaml + devm.sh with a `sleeper` function
    the test can run detached, then cold-start the project. Returns the
    guest-side repo path."""
    cmds = ["sleeper", *extra_commands]
    workspace.write_devmyaml(
        repos={
            "main": {
                "url": workspace.bare_repo_url(),
                "primary": True,
                "commands": cmds,
            },
        },
    )
    workspace.write_devm_sh(
        "#!/usr/bin/env bash\n"
        "set -eo pipefail\n"
        "sleeper() {\n"
        "  echo \"sleeper up pid=$$\"\n"
        "  sleep 600\n"
        "}\n"
    )
    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()!r}"
    return f"/home/devm/{workspace.bare_repo_label()}"


def _start_detached_run(devm, workspace, guest_repo_path, name) -> int:
    """Kick off `gdevm run -d <name>` from the guest-side repo and
    return the PID gdevm serve reported."""
    r = _ggexec(
        devm.path, workspace.path,
        ["bash", "-c", f"cd {guest_repo_path} && gdevm run -d {name}"],
        timeout=30,
    )
    assert r.returncode == 0, (
        f"gdevm run -d {name} failed: stdout={r.stdout.decode()!r} "
        f"stderr={r.stderr.decode()!r}"
    )
    m = re.search(r"pid (\d+)", r.stdout.decode())
    assert m is not None, (
        f"could not parse pid from `gdevm run -d` stdout: {r.stdout.decode()!r}"
    )
    return int(m.group(1))


@pytest.mark.timeout(600)
def test_gdevm_run_rediscovery_happy_stop(workspace, devm):
    """Happy path: start → systemctl restart gdevm-serve → gdevm runs
    STILL lists the run under the same pid and marked orphan → gdevm
    stop kills it cleanly."""
    try:
        guest_repo = _cold_start_with_sleeper(devm, workspace)
        pid_before = _start_detached_run(devm, workspace, guest_repo, "sleeper")

        # Baseline: gdevm runs sees it, pre-restart.
        r = _ggexec(devm.path, workspace.path, ["gdevm", "runs"], timeout=15)
        assert r.returncode == 0, r.stderr.decode()
        pre = r.stdout.decode()
        assert "sleeper" in pre, f"expected sleeper in pre-restart runs:\n{pre}"
        assert _parse_runs_pid(pre, "sleeper") == pid_before, pre

        # Restart gdevm-serve.service. KillMode=process keeps the bash
        # child alive; rediscovery picks it up on startup.
        r = _ggexec(
            devm.path, workspace.path,
            ["sudo", "systemctl", "restart", "gdevm-serve.service"],
            timeout=30,
        )
        assert r.returncode == 0, (
            f"systemctl restart gdevm-serve: stderr={r.stderr.decode()!r}"
        )

        status = _wait_for_serve_active(devm.path, workspace.path)
        assert status == "active", f"gdevm-serve never became active: {status!r}"

        # Child is still alive under its pre-restart pid. /proc check is
        # unprivileged — kill -0 from the devm user can't signal the
        # root-owned bash child (EPERM) and is useless as a liveness probe.
        r = _ggexec(
            devm.path, workspace.path,
            ["bash", "-c", f"test -d /proc/{pid_before}"],
            timeout=10,
        )
        assert r.returncode == 0, (
            f"bash child (pid {pid_before}) did not survive gdevm-serve restart — "
            f"KillMode=process is not keeping children alive as intended. "
            f"stdout={r.stdout.decode()!r} stderr={r.stderr.decode()!r}"
        )

        # Rediscovery: gdevm runs lists it with the SAME pid, marked orphan.
        r = _ggexec(devm.path, workspace.path, ["gdevm", "runs"], timeout=15)
        assert r.returncode == 0, r.stderr.decode()
        post = r.stdout.decode()
        assert "sleeper" in post, (
            f"rediscovery failed: `gdevm runs` after restart does not list "
            f"sleeper:\n{post}"
        )
        assert _parse_runs_pid(post, "sleeper") == pid_before, (
            f"rediscovered run's pid differs from pre-restart "
            f"({pid_before} vs. observed):\n{post}"
        )
        assert "orphan" in post, (
            f"rediscovered run should be marked orphan in MODE column:\n{post}"
        )

        # Happy stop path: gdevm stop kills the orphan via its pgroup.
        r = _ggexec(
            devm.path, workspace.path,
            ["gdevm", "stop", "sleeper"],
            timeout=30,
        )
        assert r.returncode == 0, (
            f"gdevm stop failed on rediscovered run: "
            f"stderr={r.stderr.decode()!r}"
        )

        # Child gone from the guest pid table (/proc check works
        # regardless of ownership).
        deadline = time.time() + 10
        while time.time() < deadline:
            r = _ggexec(
                devm.path, workspace.path,
                ["bash", "-c", f"test -d /proc/{pid_before}"],
                timeout=10,
            )
            if r.returncode != 0:
                break
            time.sleep(0.5)
        else:
            raise AssertionError(
                f"pid {pid_before} still alive after gdevm stop sleeper"
            )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=120,
        )


@pytest.mark.timeout(600)
def test_gdevm_run_rediscovery_kill_path(workspace, devm):
    """Kill path: start → systemctl restart gdevm-serve → rediscovered
    run's pgroup is SIGKILLed OUT-OF-BAND (not via gdevm stop); the
    poller detects ESRCH and `gdevm runs` eventually reports exit."""
    try:
        guest_repo = _cold_start_with_sleeper(devm, workspace)
        pid_before = _start_detached_run(devm, workspace, guest_repo, "sleeper")

        r = _ggexec(
            devm.path, workspace.path,
            ["sudo", "systemctl", "restart", "gdevm-serve.service"],
            timeout=30,
        )
        assert r.returncode == 0, r.stderr.decode()
        status = _wait_for_serve_active(devm.path, workspace.path)
        assert status == "active", f"gdevm-serve never became active: {status!r}"

        # Confirm rediscovery picked it up — otherwise the kill-path
        # test is testing nothing.
        r = _ggexec(devm.path, workspace.path, ["gdevm", "runs"], timeout=15)
        assert "sleeper" in r.stdout.decode(), (
            f"rediscovery did not register sleeper:\n{r.stdout.decode()!r}"
        )

        # External SIGKILL on the pgroup. Negative pid = pgroup;
        # `--` terminates options so util-linux kill parses the leading
        # `-` as pid, not as a signal name.
        r = _ggexec(
            devm.path, workspace.path,
            ["sudo", "kill", "-9", "--", f"-{pid_before}"],
            timeout=10,
        )
        assert r.returncode == 0, (
            f"external SIGKILL on pgroup failed: "
            f"stdout={r.stdout.decode()!r} stderr={r.stderr.decode()!r}"
        )

        # Confirm child gone from the pid table (/proc check is
        # unprivileged and does not depend on process ownership).
        deadline = time.time() + 5
        while time.time() < deadline:
            r = _ggexec(
                devm.path, workspace.path,
                ["bash", "-c", f"test -d /proc/{pid_before}"],
                timeout=10,
            )
            if r.returncode != 0:
                break
            time.sleep(0.2)
        else:
            raise AssertionError(f"pid {pid_before} still alive after SIGKILL")

        # Poller catches up and gdevm runs reflects exit. Default poll
        # interval is 2s; allow up to 10s before giving up. The run row
        # transitions from `running` -> `exit ?` once the poller sees
        # ESRCH; we accept either the row disappearing or STATUS
        # showing exit.
        deadline = time.time() + 15
        saw_exit = False
        last_out = ""
        while time.time() < deadline:
            r = _ggexec(devm.path, workspace.path, ["gdevm", "runs"], timeout=15)
            out = r.stdout.decode()
            last_out = out
            if "sleeper" not in out:
                saw_exit = True
                break
            for line in out.splitlines():
                if line.startswith("sleeper") and ("exit" in line):
                    saw_exit = True
                    break
            if saw_exit:
                break
            time.sleep(1)
        assert saw_exit, (
            f"poller never reflected external kill in `gdevm runs`; last "
            f"output:\n{last_out!r}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=120,
        )
