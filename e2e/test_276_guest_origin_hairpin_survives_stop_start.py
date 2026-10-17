"""276: in-guest `.test` hairpin keeps working across a `devm stop` + `devm start` cycle.

A real reproduction from a user session: after a project was stopped
and restarted (daemon lifetime unchanged), traffic from inside the VM
to the project's own `*.test` services started getting TCP RST — the
"connection refused immediately" symptom. The Mac-side view of the
project proxy stayed reachable (`https://sewtrue.test` from the host
returned a backend 502), but the in-guest hairpin via softnet's
intercepted address was dead: softnet was forwarding to the pre-stop
guest-origin ephemeral port, which the daemon no longer accepted on.

The feature contract is exercised by test_157 for a single cold-start.
This test adds the lifecycle gap: a `devm stop` must not leave softnet
pointing at a dead guest-origin listener for the next `devm start`.

What this pins:
  - Cold-start: `curl -fsS https://<api-hostname>/` from inside the
    guest succeeds (same assertion as test_157 — the baseline).
  - After `devm.stop(yes=True)` + `devm.reconcile()`-less `devm start`,
    the same curl succeeds. Pre-fix: it fails because softnet forwards
    guest hairpin traffic to a Mac-side port the daemon isn't listening
    on, surfacing as curl exit 7 (connection refused) or 35 (TLS error).
  - The guest-origin listener's ports after the restart differ from
    before (the daemon can't just leave the old pair wired and expect
    softnet to carry over).

Mechanism, best current understanding:
  - `/vm/stop` calls `StopProjectListeners` (internal/serviceapi/vm.go:1089),
    which closes the http.Servers and removes the project from
    `ProxyServer.perProj`.
  - On the next `/vm/start`, `StartGuestOriginListeners` finds no
    perProj entry and binds a fresh ephemeral pair — but the fresh
    softnet process can end up with cached forward targets from the
    pre-stop invocation; or the push ordering between `info.GuestHTTPPort`
    recorded on the daemon and the softnet `setPolicy` payload is
    off, leaving softnet dialling a port the daemon isn't bound to.
    This test pins the observable gap; the exact wiring fix belongs
    with the daemon code.
"""
from __future__ import annotations

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


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_guest_origin_hairpin_survives_stop_start(workspace, devm, sandbox_name):
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

    # ---- Phase 2: stop + start, hairpin must still work. ----
    stop = devm.stop(yes=True, timeout=120.0)
    assert stop.returncode == 0, (
        f"devm stop failed: rc={stop.returncode} "
        f"stderr={stop.stderr.decode()!r}"
    )

    restart = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=480,
    )
    assert restart.returncode == 0, (
        f"devm start (post-stop restart) failed:\nstderr={restart.stderr.decode()!r}"
    )

    # Headline assertion — the regression check. Pre-fix: curl 7
    # (connection refused) because softnet is dialling a dead port.
    second = _curl_hairpin_until(devm.path, str(workspace.path), api_hostname, deadline_s=60)
    assert second.returncode == 0, (
        f"post-restart hairpin broken — curl -fsS https://{api_hostname}/ "
        f"failed after devm stop + devm start: "
        f"rc={second.returncode} stderr={second.stderr.decode()!r}. "
        f"This is the user-observed regression: softnet forwards guest "
        f"hairpin traffic to a Mac-side port the daemon isn't listening on."
    )
    assert second.stdout.decode() == API_BODY, (
        f"post-restart hairpin body mismatch: {second.stdout.decode()!r}"
    )
