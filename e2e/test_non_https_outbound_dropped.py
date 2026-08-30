"""Pin: guest-originated TCP on ports other than :80/:443 is dropped
under FORWARDING, both when the daemon-side policy authority is
restricted AND when it is passthrough — passthrough only relaxes the
per-request allow-check, it never reopens raw ports.

Regression fence for the intentional trade-off documented in the
always-through-iron-proxy spec's §Trade-offs: v0.21.0's OPEN softnet
policy let install:/startup: scripts reach arbitrary ports (git-over-
SSH :22, custom TCP protocols) direct; FORWARDING never does, in
either daemon-side mode.

Test: from inside the guest, attempt a raw TCP connect to
example.com:22 and example.com:3306 (bash's `/dev/tcp` pseudo-device —
no `nc`/extra packages needed, same technique test_157 uses for its
loopback direct-service check). Both must fail under both restricted
and passthrough. A positive control — `curl https://example.com/`
succeeds under passthrough — proves the port failures are a targeted
non-HTTPS drop, not a general network outage.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


def _tcp_connect_ok(sandbox, host: str, port: int, timeout_s: int = 3) -> bool:
    r = sandbox.exec_shell(
        f"timeout {timeout_s} bash -c 'exec 3<>/dev/tcp/{host}/{port}' "
        f"&& echo DEVM_TCP_OK || echo DEVM_TCP_FAIL"
    )
    return "DEVM_TCP_OK" in r.stdout


def _https_ok(sandbox, host: str) -> bool:
    r = sandbox.exec_shell(
        f"curl -o /dev/null -s -w '%{{http_code}}' --max-time 5 https://{host}/"
    )
    return r.stdout.strip() == "200"


@pytest.mark.timeout(300)
def test_non_https_outbound_dropped(devm, workspace, sandbox_name):
    from helpers.tart import TartSandbox

    workspace.write_devmyaml(
        no_repo=True,
        network={"allow": ["example.com"]},
    )

    try:
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"
        sandbox = TartSandbox(name=sandbox_name)

        # ---- restricted: non-HTTPS ports dropped. ----
        assert not _tcp_connect_ok(sandbox, "example.com", 22), (
            "restricted: guest must NOT reach example.com:22 — non-HTTPS "
            "ports must be dropped under FORWARDING"
        )
        assert not _tcp_connect_ok(sandbox, "example.com", 3306), (
            "restricted: guest must NOT reach example.com:3306"
        )

        # ---- passthrough: still dropped — only the allow-check relaxes. ----
        r = subprocess.run(
            [devm.path, "passthrough", "--for", "60s"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, f"devm passthrough failed:\n{r.stderr.decode()}"

        assert not _tcp_connect_ok(sandbox, "example.com", 22), (
            "passthrough: guest must still NOT reach example.com:22 — "
            "passthrough relaxes the allowlist check only, it does not "
            "reopen non-HTTPS ports"
        )
        assert not _tcp_connect_ok(sandbox, "example.com", 3306), (
            "passthrough: guest must still NOT reach example.com:3306"
        )

        # ---- positive control: HTTPS still works under passthrough,
        # ---- proving the failures above are a targeted port-level
        # ---- drop, not a general network outage. ----
        assert _https_ok(sandbox, "example.com"), (
            "positive control failed: https://example.com/ should succeed "
            "under passthrough — if this also fails, the port 22/3306 "
            "failures above may just be a network outage, not the "
            "non-HTTPS drop this test pins"
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
