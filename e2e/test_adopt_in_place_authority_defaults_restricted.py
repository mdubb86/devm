"""Pin: on daemon restart, adopt-in-place forces every adopted
project's policy-authority mode back to `restricted`, regardless of
the mode the project was in when the daemon died. Mode is never
persisted across restart — only the allowlist is (from the state
snapshot); see `AdoptIronProxies` in the always-through-iron-proxy
design.

Under v0.21.0's softnet-level OPEN/ENFORCED split there was no
daemon-side "mode" to reset in the first place. Under the new gRPC
policy-authority design, adopting into anything other than
`restricted` would be a fail-open regression: a daemon crash mid
`devm passthrough` window would otherwise resurrect the open window on
every subsequent restart, forever, since the timer that would have
closed it dies with the daemon.

Test:
1. Cold-start, then open a passthrough window and confirm it is
   actually in effect (a not-allowlisted host reaches through) —
   otherwise the restart-time assertion below wouldn't prove anything.
2. `devm service restart` — kills and restarts the daemon while the
   passthrough window still has 170+s left on its timer.
3. Confirm iron-proxy survived the restart (same PID — adopted, not
   respawned fresh).
4. Hit the same not-allowlisted host again: must get devm's own 403
   (`X-Devm-Blocked`) — proof adopt-in-place reset the mode to
   restricted, not the pre-restart passthrough.

`devm service restart` shells out to `sudo` (launchctl kickstart)
internally. This lands the test in the `install` marker via conftest's
source-grep auto-detect (`'"service", "restart"'`), the same mechanism
`test_44_iron_proxy_adopted_across_daemon_restart.py` relies on — run
through `just e2e-install`, which primes sudo once for the whole run.
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


def _curl_headers(sandbox, url: str):
    return sandbox.exec_shell(
        f"curl -s -D - -o /dev/null --max-time 5 {url}"
    )


@pytest.mark.timeout(420)
@pytest.mark.slow
def test_adopt_in_place_authority_defaults_restricted(devm, workspace, sandbox_name, devm_installed):
    from helpers.tart import TartSandbox

    workspace.write_devmyaml(
        no_repo=True,
        network={"allow": ["api.github.com"]},
        packages=["curl"],
    )

    try:
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"
        sandbox = TartSandbox(name=sandbox_name)

        pid_before = _iron_proxy_pid_for(workspace.slug)
        assert pid_before is not None, (
            f"iron-proxy should be running for project {workspace.slug!r} after cold-start"
        )

        # ---- Open a passthrough window and confirm it's actually in
        # ---- effect BEFORE restart — httpbin.org isn't allowlisted, so
        # ---- this only succeeds if the mode really is passthrough. ----
        r = subprocess.run(
            [devm.path, "passthrough", "--for", "180s"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, f"devm passthrough failed:\n{r.stderr.decode()}"

        deadline = time.monotonic() + 10
        pre_restart_ok = False
        while time.monotonic() < deadline:
            r = sandbox.exec_shell(
                "curl -o /dev/null -s -w '%{http_code}' --max-time 5 https://httpbin.org/get"
            )
            if r.stdout.strip() == "200":
                pre_restart_ok = True
                break
            time.sleep(0.5)
        assert pre_restart_ok, (
            "precondition failed: passthrough should have let httpbin.org "
            "through before the daemon restart — can't test the adopt "
            "default without a real passthrough window active"
        )

        # ---- Restart the daemon mid-passthrough-window. ----
        r = subprocess.run(
            [devm.path, "service", "restart"],
            capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"service restart failed:\n{r.stderr.decode()}"

        # ---- iron-proxy survived the restart (adopted, not respawned
        # ---- fresh — same PID as before). ----
        deadline = time.monotonic() + 10
        pid_after = None
        while time.monotonic() < deadline:
            pid_after = _iron_proxy_pid_for(workspace.slug)
            if pid_after is not None:
                break
            time.sleep(0.5)
        assert pid_after == pid_before, (
            f"iron-proxy PID changed across daemon restart: "
            f"before={pid_before} after={pid_after} — adoption failed"
        )

        # ---- THE assertion: adopt-in-place must have forced the mode
        # ---- back to restricted — httpbin.org must be blocked again
        # ---- with iron-proxy's own marker header, even though the
        # ---- pre-restart mode was passthrough with 170+s left on its
        # ---- timer. ----
        deadline = time.monotonic() + 15
        blocked = False
        last = ""
        while time.monotonic() < deadline:
            r = _curl_headers(sandbox, "https://httpbin.org/get")
            last = r.stdout
            if "x-devm-blocked" in r.stdout.lower():
                blocked = True
                break
            time.sleep(0.5)
        assert blocked, (
            "adopt-in-place must reset authority mode to restricted — "
            "httpbin.org should be blocked (with X-Devm-Blocked) after "
            "daemon restart even though the project was mid-passthrough "
            "when the daemon restarted; last response:\n" + last
        )
    finally:
        subprocess.run(
            [devm.path, "restrict"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
