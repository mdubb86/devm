"""44: iron-proxy survives `devm service restart` (adoption round-trip).

Iron-proxy is intentionally setsid'd on spawn so it outlives daemon
death — the running VM keeps egress enforcement even if the daemon
crashes. On daemon startup, DiscoverIronProxies + Supervisor.Adopt
re-attach to the still-running process so /vm/stop and /vm/status
behave correctly post-restart instead of orphaning it.

What this pins:
  - Cold-start spawns one iron-proxy whose config path encodes the
    project_id (`<runtime_dir>/iron-proxy/<slug>.yaml`).
  - `devm service restart` does NOT kill iron-proxy (same PID before
    and after).
  - Both softnet listeners (`:81` pop, `:82`
    propose/passthrough/refresh-bundle/recipes) are reachable from
    inside the running guest after the daemon restart — proves the
    adopt path re-binds them and re-pushes softnet's forward-targets
    so guest-initiated flows survive a restart. Without this, every
    gdevm command from the guest hits `connection refused` on the
    stale ports the pre-restart daemon had bound.
  - `gdevm propose` succeeds end-to-end from inside the guest after
    the daemon restart — proves the adopt path rehydrates
    `cache.MacCwd` from the persisted StateSnapshot. Without this,
    every propose/passthrough/approve call returns 412 "project not
    started" because the precondition check reads a cache field that
    stays empty across the restart otherwise. Strictly stronger than
    the /dev/tcp probe above (which only proves listeners are
    reachable, not that request handlers can serve their preconditions).
  - The post-restart daemon can `teardown` cleanly — the adopted
    process is stopped, not orphaned.

What it doesn't cover (tested elsewhere):
  - Egress enforcement itself -> test_43.
  - Adoption when the prior daemon crashed (vs. a clean restart):
    not pinned; same code path though, since `service restart` does
    a stop+start with no graceful handoff.
"""
from __future__ import annotations

import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


def _iron_proxy_pid_for(project_id: str) -> int | None:
    """Return the PID of the iron-proxy process for this project, or None."""
    r = subprocess.run(
        ["ps", "-axo", "pid=,command="],
        capture_output=True, text=True, check=True,
    )
    needle = f"/iron-proxy/{project_id}.yaml"
    for line in r.stdout.splitlines():
        if needle in line:
            return int(line.strip().split(None, 1)[0])
    return None


@pytest.mark.timeout(420)
@pytest.mark.slow
def test_iron_proxy_survives_daemon_restart(devm, workspace, sandbox_name, devm_installed):
    workspace.write_devmyaml(
        no_repo=True,
        services={"sleep": {"exec": ["/bin/sleep", "infinity"], "restart": "always"}},
        network={"allow": ["httpbin.org"]},
    )
    workspace.write_devm_sh(
        "#!/usr/bin/env bash\n"
        "set -eo pipefail\n"
        "install() {\n"
        "  true\n"
        "}\n"
    )

    try:
        # Cold-start: spawns iron-proxy as part of /vm/start.
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        pid_before = _iron_proxy_pid_for(workspace.slug)
        assert pid_before is not None, \
            f"iron-proxy should be running for project {workspace.slug!r} after cold-start"

        # Restart the daemon. `service restart` shells out to sudo
        # internally for the launchctl kickstart; the sudo_capable
        # fixture has already verified the env can prompt.
        r = subprocess.run(
            [devm.path, "service", "restart"],
            capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"service restart failed:\n{r.stderr.decode()}"

        # Give the new daemon a moment to run DiscoverIronProxies + Adopt.
        # The daemon waits 5s for /health on its end before declaring
        # restart successful (see service.go: 'service did not become
        # healthy within 5s'), so by the time restart returned the
        # daemon was already serving. A short settle covers any race
        # between /health responding and the adoption loop finishing.
        time.sleep(2)

        pid_after = _iron_proxy_pid_for(workspace.slug)
        assert pid_after == pid_before, (
            f"iron-proxy PID changed across daemon restart: "
            f"before={pid_before} after={pid_after} — adoption failed"
        )

        # Softnet-listener rebind on adopt. The softnet child process
        # survived the daemon restart and still holds its last
        # forward-target push from the OLD daemon — the ports it
        # forwards :81/:82 to are the OLD daemon's popLn/proposeLn
        # ports, which are now dead. The adopt path in runner.go must
        # re-bind fresh listeners AND re-push softnet's forward map,
        # or every gdevm-initiated call from the guest hits connection
        # refused. Test /dev/tcp against both ports so the regression
        # is caught for the pop AND propose surfaces (which serve
        # /pop, /propose, /passthrough, /refresh-bundle, /recipes/*).
        for port, name in [(81, "pop"), (82, "propose/passthrough/recipes")]:
            probe = subprocess.run(
                [devm.path, "exec", "bash", "-c", f"exec 3<>/dev/tcp/192.168.127.1/{port}"],
                cwd=str(workspace.path), capture_output=True, timeout=15,
            )
            assert probe.returncode == 0, (
                f"softnet :{port} ({name}) unreachable from guest after daemon restart:\n"
                f"{probe.stderr.decode()}\n"
                "The adopt path in internal/serviceapi/runner.go must re-bind "
                "pop + propose listeners and re-push softnet's forward-target map."
            )

        # Cache-state rehydrate. `gdevm propose` hits /propose on the
        # softnet :82 forward, whose handler reads cache.ProjectRow.MacCwd
        # and rejects with 412 "project not started" if empty. MacCwd is
        # stamped once at /vm/start and no watchdog check reconciles it,
        # so a fresh-daemon cache keeps it empty across a restart unless
        # rehydrateCacheFromStateSnapshots restores it from the persisted
        # StateSnapshot. This is a strict superset of the /dev/tcp probe:
        # the probe proves the port is reachable, this proves the handler
        # can serve real requests with correct state.
        propose = subprocess.run(
            [devm.path, "exec", "gdevm", "propose", "--reason", "post-restart adopt smoke"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert propose.returncode == 0, (
            f"gdevm propose failed after daemon restart (rc={propose.returncode}):\n"
            f"stdout: {propose.stdout.decode()!r}\n"
            f"stderr: {propose.stderr.decode()!r}\n"
            "The adopt path must rehydrate cache.MacCwd from the persisted "
            "StateSnapshot — otherwise every guest-initiated propose / "
            "passthrough / approve call returns 412 until the user runs "
            "`devm start` again."
        )

        # Adopted process can be stopped via the regular teardown path.
        r = subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=120,
        )
        assert r.returncode == 0, f"teardown after restart failed:\n{r.stderr.decode()}"

        # And iron-proxy is actually gone (SIGTERM delivered, process exited).
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline:
            if _iron_proxy_pid_for(workspace.slug) is None:
                break
            time.sleep(0.2)
        assert _iron_proxy_pid_for(workspace.slug) is None, \
            "iron-proxy still running after teardown of adopted process"
    finally:
        # Best-effort cleanup if the test bailed early.
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
