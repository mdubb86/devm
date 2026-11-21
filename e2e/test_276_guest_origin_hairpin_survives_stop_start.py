"""276: in-guest `.test` hairpin survives a watchdog proxy-listener respawn.

Observed on a real workstation session: without any `devm stop` / `vm/stop`,
in-guest curl to a project's own `*.test` services started getting TCP RST.
The Mac-side view (`https://<project>.test` from the host) kept working, but
softnet was still forwarding guest hairpin traffic to the pre-respawn
guest-origin ephemeral port — on the Mac, nothing was bound there any more.

Mechanism: a watchdog proxy-listener probe failed twice in a row, so
`watchdog_check_proxy_listener.Run` called `GroundTruth.RespawnProxyListeners`
(internal/serviceapi/watchdog_ground_truth.go). That respawner calls
`ProxyServer.StopProjectListeners` (closes BOTH the project :80/:443
listeners AND the guest-origin ephemeral pair) and then only
`ProxyServer.StartProjectListeners` — so the project listener comes back,
but the guest-origin pair never does. Softnet keeps its pre-respawn
forward targets, and every subsequent guest-hairpin dial lands on a port
the daemon isn't listening on.

What this pins:
  - Cold-start baseline: `curl -fsS https://<api>/` from inside the guest
    succeeds (same shape as test_157).
  - After a watchdog proxy-listener respawn fires for this project, the
    same curl must still succeed. Pre-fix: softnet dials a dead Mac-side
    port and curl exits 7 (connection refused) / 35 (TLS error).

How this triggers the respawn (no test-only daemon hooks):
  - `watchdog_check_proxy_listener` probes gdevm-serve's /v1/health via
    the Mac-side project listener. We stop the in-guest `gdevm-serve`
    unit so the probe fails on both the initial attempt and its retry;
    the watchdog then invokes `RespawnProxyListeners`.
  - The watchdog ticks every 60s (runner.go:303), so after stopping the
    unit the test waits up to ~90s for the "respawned but still unhealthy"
    daemon log line (watchdog_check_proxy_listener.go:73) scoped to this
    project. The log line is the ground-truth signal that the respawn
    actually ran — not a sleep guess.
  - Once seen, the test re-starts gdevm-serve and immediately exercises
    the in-guest hairpin. The project listener is healthy again (the
    respawn fixed it); only the guest-origin pair is still dead, so the
    curl exercises exactly the regression.
"""
from __future__ import annotations

import os
import pathlib
import subprocess
import time

import pytest

from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm

API_PORT = 54730

API_BODY = "guest-origin-hairpin-e2e-ok"
_API_RESPONSE = (
    "HTTP/1.1 200 OK\\r\\n"
    f"Content-Length: {len(API_BODY)}\\r\\n"
    "Connection: close\\r\\n"
    "\\r\\n"
    f"{API_BODY}"
)

# e2e daemon log location — see internal/identity/identity.go (Name = "devm-e2e")
# redirected to ~/Library/Logs/com.devm.e2e.service.{out,err}.log by launchd.
E2E_DAEMON_ERR_LOG = (
    pathlib.Path(os.path.expanduser("~"))
    / "Library" / "Logs" / "com.devm.e2e.service.err.log"
)

# Watchdog ticks every 60s (internal/serviceapi/runner.go:303). After we stop
# gdevm-serve the next tick is up to 60s away, and the tick itself can take
# up to ~7s (probe + 500ms retry + probe + respawn + 250ms recheck + probe,
# each probe bounded by the 2s healthProbeClient timeout). 90s absorbs that
# without pinning on an exact ticker alignment.
RESPAWN_LOG_WAIT_S = 90


def _curl_hairpin(devm_path: str, workspace_path: str, hostname: str) -> subprocess.CompletedProcess:
    """One in-guest curl against the hairpin. No -k — the guest trusts the devm CA."""
    return devm_exec_with_retry(
        devm_path,
        ["curl", "-fsS", "--max-time", "10", f"https://{hostname}/"],
        cwd=workspace_path, timeout=20,
    )


def _curl_hairpin_until(devm_path: str, workspace_path: str, hostname: str, deadline_s: float) -> subprocess.CompletedProcess:
    """Poll until the hairpin succeeds or the deadline fires. The service's
    systemd unit may not have its listener bound the instant start finishes,
    so cold-start assertions retry — but the retry ceiling is bounded so a
    broken hairpin surfaces as a failure, not a hang."""
    deadline = time.time() + deadline_s
    result = _curl_hairpin(devm_path, workspace_path, hostname)
    while result.returncode != 0 and time.time() < deadline:
        time.sleep(1)
        result = _curl_hairpin(devm_path, workspace_path, hostname)
    return result


def _wait_for_respawn_log(log_path: pathlib.Path, start_offset: int, project: str, deadline_s: float) -> bool:
    """Poll the daemon stderr log from start_offset for the proxy-listener
    respawn warning scoped to this project. The exact format is
    `watchdog: drift on proxy-listener for <project>: healthy → false,
    respawned ...` (see watchdog_check_proxy_listener.go:71-73).

    Either "respawned and recovered" or "respawned but still unhealthy"
    proves the respawn ran. We accept both so the test doesn't depend on
    whether gdevm-serve happened to come back between the probe and the
    recheck — only on whether the respawn code path executed.
    """
    needle_fragment = f"drift on proxy-listener for {project}: healthy → false, respawned"
    deadline = time.time() + deadline_s
    while time.time() < deadline:
        try:
            with log_path.open("rb") as f:
                f.seek(start_offset)
                chunk = f.read().decode("utf-8", errors="replace")
        except FileNotFoundError:
            chunk = ""
        if needle_fragment in chunk:
            return True
        time.sleep(1)
    return False


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_guest_origin_hairpin_survives_watchdog_respawn(workspace, devm, sandbox_name):
    api_hostname = f"api.{sandbox_name}.e2e.test"

    api_script = f"while true; do printf '{_API_RESPONSE}' | nc -l -p {API_PORT}; done"

    workspace.write_devmyaml(
        no_repo=True,
        packages=["netcat-openbsd"],
        network={"allow": ["deb.debian.org", "security.debian.org"]},
        services={
            "api": {
                "port": API_PORT,
                "hostname": api_hostname,
                "exec": ["sh", "-c", api_script],
                "restart": "always",
            },
        },
    )

    # ---- Phase 1: cold-start, hairpin alive. Baseline — same shape as test_157. ----
    cold = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=480,
    )
    assert cold.returncode == 0, (
        f"devm start (cold-start) failed:\nstderr={cold.stderr.decode()!r}"
    )

    first = _curl_hairpin_until(devm.path, str(workspace.path), api_hostname, deadline_s=60)
    assert first.returncode == 0, (
        f"baseline cold-start: curl -fsS https://{api_hostname}/ failed: "
        f"rc={first.returncode} stderr={first.stderr.decode()!r}"
    )
    assert first.stdout.decode() == API_BODY, (
        f"baseline cold-start body: {first.stdout.decode()!r}"
    )

    # ---- Phase 2: force a watchdog proxy-listener respawn. ----
    #
    # Record the daemon stderr log's current size as a start offset — anything
    # we see past this point is new. Any log-rotation between here and the
    # wait call surfaces as "file size shrank" and we start from 0 instead.
    log_start_offset = E2E_DAEMON_ERR_LOG.stat().st_size if E2E_DAEMON_ERR_LOG.exists() else 0

    # Stop gdevm-serve inside the guest. The daemon's proxy-listener probe
    # hits /v1/health via the Mac-side project listener → softnet → guest →
    # gdevm-serve. With gdevm-serve stopped, the probe fails on both the
    # initial attempt and the 500ms-later retry, and the watchdog respawns
    # the project's :80/:443 listeners — destroying the guest-origin pair
    # as a side effect of StopProjectListeners.
    stop_serve = devm_exec_with_retry(
        devm.path,
        ["sudo", "systemctl", "stop", "gdevm-serve.service"],
        cwd=str(workspace.path), timeout=30,
    )
    assert stop_serve.returncode == 0, (
        f"in-guest `systemctl stop gdevm-serve` failed: "
        f"rc={stop_serve.returncode} stderr={stop_serve.stderr.decode()!r}"
    )

    try:
        saw_respawn = _wait_for_respawn_log(
            E2E_DAEMON_ERR_LOG, log_start_offset, sandbox_name, RESPAWN_LOG_WAIT_S,
        )
        assert saw_respawn, (
            f"expected watchdog proxy-listener respawn for {sandbox_name} within "
            f"{RESPAWN_LOG_WAIT_S}s of stopping gdevm-serve, but no "
            f"'drift on proxy-listener for {sandbox_name}: healthy → false, respawned ...' "
            f"line appeared in {E2E_DAEMON_ERR_LOG}. The test setup can't "
            f"reach the regression without the respawn — investigate whether "
            f"the watchdog is running (daemon up?) and whether the probe is "
            f"actually failing (gdevm-serve really stopped?)."
        )
    finally:
        # Always restore gdevm-serve so the sandbox is usable for teardown
        # and so the Mac-side project listener returns to healthy on the
        # next watchdog tick.
        subprocess.run(
            [devm.path, "exec", "sudo", "systemctl", "start", "gdevm-serve.service"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )

    # ---- Phase 3: headline regression check. ----
    #
    # The guest-origin ephemeral listener pair was torn down by the respawn's
    # StopProjectListeners and never recreated. Softnet still holds the
    # pre-respawn forward targets, so an in-guest curl via the hairpin
    # (DNS → 192.0.2.2 → softnet → vsock → dead Mac-side port) must still
    # succeed only once the daemon re-creates the guest-origin pair as part
    # of RespawnProxyListeners. Pre-fix: curl exits 7 (connection refused)
    # or 35 (TLS error) because softnet is dialling a port the daemon isn't
    # listening on.
    second = _curl_hairpin_until(devm.path, str(workspace.path), api_hostname, deadline_s=60)
    assert second.returncode == 0, (
        f"post-respawn hairpin broken — curl -fsS https://{api_hostname}/ "
        f"failed after a watchdog proxy-listener respawn: "
        f"rc={second.returncode} stderr={second.stderr.decode()!r}. "
        f"This is the user-observed regression: softnet still forwards "
        f"guest hairpin traffic to the pre-respawn guest-origin port, which "
        f"the daemon no longer accepts on. The fix is in "
        f"internal/serviceapi/watchdog_ground_truth.go:RespawnProxyListeners "
        f"— it must also re-create the guest-origin pair, not just the "
        f"project :80/:443 listeners."
    )
    assert second.stdout.decode() == API_BODY, (
        f"post-respawn hairpin body mismatch: {second.stdout.decode()!r}"
    )
