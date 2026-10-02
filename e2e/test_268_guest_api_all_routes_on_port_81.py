"""268: the per-project guest-API listener on 192.168.127.1:81 serves
EVERY gdevm → Mac route under one mux (/pop, /propose, /passthrough,
/refresh-bundle, /recipes/list), and the retired port 82 is actually
refused by softnet.

Proof shape: raw curl from inside the guest hits each route and asserts
the handler responded with SOMETHING (any HTTP status, even 4xx). A
status of 000 means curl couldn't connect — softnet didn't forward
→ the listener isn't wired or the softnet gateway:81 hairpin regressed.
Then curl port 82 and assert it IS 000 (softnet denies, the gateway:82
hairpin is gone).

This test doesn't exercise each handler's business logic (test_267 does
that for /pop end-to-end, and the Go unit tests cover /propose etc.) —
its job is only to prove the port collapse: one listener, five routes,
port 82 dead."""
from __future__ import annotations
import subprocess

import pytest

pytestmark = pytest.mark.devm


def _guest_curl(devm, workspace, url: str, method: str = "POST") -> tuple[int, int]:
    """Run `curl -sS -X <method> --max-time 5 -o /dev/null -w %{http_code} <url>`
    inside the guest. Returns (curl_exit_code, http_status_from_output).
    http_status is 0 when the dial failed (connection refused, timeout)."""
    r = subprocess.run(
        [devm.path, "exec", "bash", "-c",
         f"curl -sS -X {method} --max-time 5 -o /dev/null -w '%{{http_code}}' {url}"],
        cwd=str(workspace.path), capture_output=True, timeout=20,
    )
    # devm exec prints its own startup lines; the curl output is clean on
    # the final line because -o /dev/null + -w prints exactly the status.
    out = r.stdout.decode().strip()
    try:
        status = int(out.splitlines()[-1])
    except (ValueError, IndexError):
        status = 0
    return r.returncode, status


@pytest.mark.timeout(240)
def test_guest_api_all_routes_on_port_81(devm, workspace):
    workspace.write_devmyaml(no_repo=True)
    try:
        r = subprocess.run([devm.path, "start"], cwd=str(workspace.path),
                           capture_output=True, timeout=180)
        assert r.returncode == 0, f"cold-start failed: {r.stderr.decode()!r}"

        # Every route must respond on port 81. We don't care about the
        # exact status — the handlers reject empty/malformed bodies with
        # 4xx, which proves the handler saw the request. A 000 means
        # curl couldn't dial, which is the regression we're guarding.
        routes = [
            ("/pop", "POST"),
            ("/propose", "POST"),
            ("/passthrough", "POST"),
            ("/refresh-bundle", "POST"),
            ("/recipes/list", "GET"),
        ]
        for path, method in routes:
            _, status = _guest_curl(devm, workspace, f"http://192.168.127.1:81{path}", method)
            assert status != 0, (
                f"port 81 {path} ({method}): dial failed — the guest-API "
                f"listener isn't reachable, or the softnet gateway:81 "
                f"hairpin regressed."
            )
            # Being extra pedantic — HTTP codes are 100..599; a value
            # outside that range would be a parsing failure above.
            assert 100 <= status <= 599, f"port 81 {path}: parsed garbage status {status}"

        # Port 82 is the retired propose port. Softnet now denies the
        # gateway:82 hairpin, so curl should fail with no HTTP response.
        _, status_82 = _guest_curl(devm, workspace, "http://192.168.127.1:82/propose", "POST")
        assert status_82 == 0, (
            f"port 82 /propose returned HTTP {status_82} but should be refused — "
            f"softnet still has a stale gateway:82 forward target, or a legacy "
            f"listener is still bound."
        )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                       capture_output=True, timeout=60)
