"""138: iron-proxy survives SIGTERM to its shim (pexec-managed path).

Regression pin for the everstone/buzztrack Bug B: `launchctl bootout`
of the daemon during devm install/upgrade walks the daemon's session
and delivers SIGTERM to every descendant sharing that session,
including the setsid shim. The old shim forwarded SIGTERM to
iron-proxy → iron-proxy shut down cleanly → project egress died.

The fix: shim ignores SIGTERM (and HUP/INT/QUIT). Iron-proxy — in its
own session via setsid — never sees the signal at all. Shim keeps
running until iron-proxy exits naturally (via supervisor.Stop
signaling iron-proxy's PID directly).

This test simulates the bootout by sending SIGTERM directly to the
shim PID and asserting:
  1. Shim keeps running (didn't die on SIGTERM).
  2. Iron-proxy keeps running (didn't receive a forwarded signal).

Then it exercises the Stop path (via `devm teardown`) and asserts
iron-proxy actually dies — proving supervisor.Stop reaches
iron-proxy directly despite the shim ignoring signals.

Pairs with test_139 which covers the same shim + Stop invariants for
the ADOPTED supervisor path (after `devm service restart`).
"""
from __future__ import annotations

import os
import signal
import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


def _iron_proxy_pids_for(project_id: str) -> tuple[int | None, int | None]:
    """Return (shim_pid, iron_proxy_pid) or (None, None) if not both found.

    Shim = process whose command contains devm-setsid-shim AND this
    project's iron-proxy config path.
    Iron-proxy = process whose command STARTS with iron-proxy binary
    AND references this project's config path.
    """
    r = subprocess.run(
        ["ps", "-axo", "pid=,command="],
        capture_output=True, text=True, check=True,
    )
    needle = f"/iron-proxy/{project_id}.yaml"
    shim_pid: int | None = None
    ip_pid: int | None = None
    for line in r.stdout.splitlines():
        if needle not in line:
            continue
        pid = int(line.strip().split(None, 1)[0])
        if "devm-setsid-shim" in line:
            shim_pid = pid
        else:
            ip_pid = pid
    return shim_pid, ip_pid


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_iron_proxy_survives_shim_sigterm_pexec_managed(
    devm, workspace, sandbox_name,
):
    workspace.write_devmyaml(
        install=["true"],
        network={"allow": ["httpbin.org"]},
    )

    try:
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        shim_pid, ip_pid = _iron_proxy_pids_for(workspace.slug)
        assert shim_pid and ip_pid, (
            f"expected shim + iron-proxy PIDs, got shim={shim_pid} ip={ip_pid}"
        )

        # SIGTERM the shim — mirrors launchctl bootout's descendant walk
        # during `devm install`/upgrade. The old shim would forward this
        # to iron-proxy AND die itself.
        os.kill(shim_pid, signal.SIGTERM)

        # Give the signal time to propagate. If shim were still going to
        # exit or forward, it'd happen well within 1s.
        deadline = time.monotonic() + 1.5
        while time.monotonic() < deadline:
            try:
                os.kill(shim_pid, 0)  # shim alive?
                os.kill(ip_pid, 0)    # iron-proxy alive?
            except ProcessLookupError as e:
                pytest.fail(
                    f"process died on SIGTERM to shim: {e}. "
                    f"shim_pid={shim_pid}, ip_pid={ip_pid}. "
                    f"Shim must ignore SIGTERM and must NOT forward it — "
                    f"see cmd/devm-setsid-shim/main.go."
                )
            time.sleep(0.1)

        # Both alive after the survival window. Now exercise the Stop
        # path — `devm teardown` calls supervisor.Stop which must
        # signal iron-proxy's PID directly (not the shim) and actually
        # kill it. If Stop instead relied on pexec signaling the shim
        # (which now ignores), iron-proxy would linger.
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
        # Iron-proxy should be gone within a bounded window.
        deadline = time.monotonic() + 15
        while time.monotonic() < deadline:
            try:
                os.kill(ip_pid, 0)
            except ProcessLookupError:
                return
            time.sleep(0.2)
        pytest.fail(
            f"iron-proxy (pid={ip_pid}) still alive 15s after `devm teardown`. "
            f"supervisor.Stop should have signaled iron-proxy directly "
            f"via s.childPIDs[k]. Check SpawnIronProxy's SetChildPID call."
        )
    finally:
        # Belt + suspenders: kill any lingering iron-proxy/shim if the
        # assertions above tripped before teardown completed.
        for pid in _iron_proxy_pids_for(workspace.slug):
            if pid:
                try:
                    os.kill(pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
