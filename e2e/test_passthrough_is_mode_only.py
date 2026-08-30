"""Pin: `devm passthrough` flips only the daemon-side policy-authority
mode; softnet stays FORWARDING throughout. Passthrough windows are
strictly better than the surrounding restricted state — still MITM'd,
still audited, still secret-substituted — not weaker.

Assertion shape: hit a not-allowlisted host and get devm's own 403
(`X-Devm-Blocked` header) — proof iron-proxy is in the path. Then
`devm passthrough`, hit the same host, get through (200). Then `devm
restrict`, hit the same host again, get devm's own 403 again. The
pre- and post- 403s both carrying `X-Devm-Blocked` is the load-bearing
evidence that softnet stayed FORWARDING the entire time — a softnet
that had reverted to a direct-route bypass mode could never produce
iron-proxy's own reject body; it would produce a raw connection
failure with no marker header at all.
"""
from __future__ import annotations

import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


def _curl_headers(sandbox, url: str):
    return sandbox.exec_shell(
        f"curl -s -D - -o /dev/null --max-time 5 {url}"
    )


@pytest.mark.timeout(300)
def test_passthrough_is_mode_only(devm, workspace, sandbox_name):
    from helpers.tart import TartSandbox

    workspace.write_devmyaml(
        no_repo=True,
        network={"allow": ["httpbin.org"]},
        packages=["curl"],
    )

    try:
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"
        sandbox = TartSandbox(name=sandbox_name)

        # ---- Baseline: httpbin.org is NOT allowlisted; iron-proxy's
        # ---- own 403 (with its marker header) proves it's in the path. ----
        r = _curl_headers(sandbox, "https://httpbin.org/get")
        assert "x-devm-blocked" in r.stdout.lower(), (
            f"baseline: httpbin.org must be blocked by iron-proxy with "
            f"X-Devm-Blocked; got:\n{r.stdout}"
        )

        # ---- devm passthrough opens the window: same host now reaches. ----
        r = subprocess.run(
            [devm.path, "passthrough", "--for", "30s"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, f"devm passthrough failed:\n{r.stderr.decode()}"

        deadline = time.monotonic() + 10
        ok = False
        last_status = None
        while time.monotonic() < deadline:
            r = sandbox.exec_shell(
                "curl -o /dev/null -s -w '%{http_code}' --max-time 5 https://httpbin.org/get"
            )
            last_status = r.stdout.strip()
            if last_status == "200":
                ok = True
                break
            time.sleep(0.5)
        assert ok, (
            f"passthrough must let httpbin.org through; last status "
            f"{last_status!r}"
        )

        # ---- devm restrict closes the window; iron-proxy's own 403
        # ---- resumes — proof softnet never left FORWARDING. ----
        r = subprocess.run(
            [devm.path, "restrict"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, f"devm restrict failed:\n{r.stderr.decode()}"

        deadline = time.monotonic() + 10
        blocked_again = False
        last = ""
        while time.monotonic() < deadline:
            r = _curl_headers(sandbox, "https://httpbin.org/get")
            last = r.stdout
            if "x-devm-blocked" in r.stdout.lower():
                blocked_again = True
                break
            time.sleep(0.5)
        assert blocked_again, (
            "post-restrict: httpbin.org must be blocked again with "
            "iron-proxy's own X-Devm-Blocked marker (a softnet that had "
            "reverted to a bypass mode could never produce this header); "
            "last response:\n" + last
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
