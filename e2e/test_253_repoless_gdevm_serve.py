"""253: cold-start a repo-less project (no_repo=True). Confirms
gdevm-serve.service is running in the guest, the reserved
_devm.<project>.test health route is reachable, and `devm status
--json` reports the proxy healthy.

Pins that gdevm-serve installs and probes unconditionally — not gated
on the presence of a `repos:` primary — matching spec Review focus #2
("Guest with no primary repo: gdevm serve should start on repo-less
projects too, since the wedge affects them equally").
"""
from __future__ import annotations

import json
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(300)
def test_repoless_project_gets_gdevm_serve(devm, workspace):
    workspace.write_devmyaml(no_repo=True)
    try:
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=240,
        )
        assert r.returncode == 0, f"cold-start failed: {r.stderr.decode()!r}"

        # gdevm-serve.service is enabled + active in the guest even
        # though this project declared no `repos:` block.
        active = subprocess.run(
            [devm.path, "exec", "systemctl", "is-active", "gdevm-serve.service"],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert active.returncode == 0 and active.stdout.decode().strip() == "active", (
            f"gdevm-serve.service not active on a repo-less project: "
            f"stdout={active.stdout.decode()!r} stderr={active.stderr.decode()!r}"
        )

        # The reserved health route is reachable from the Mac side —
        # proves the Mac-side route registration and the guest-side
        # listener both came up without a primary repo to key off of.
        # _devm.<project>.test resolves via /etc/resolver/test -> devm's
        # DNS -> the project's pool IP -> the Mac reverse proxy ->
        # softnet-exposed :8940 in the guest.
        hostname = f"_devm.{workspace.vm_name}.test"
        probe = subprocess.run(
            ["curl", "-sf", "-m", "5", f"http://{hostname}/v1/health"],
            capture_output=True, timeout=15,
        )
        assert probe.returncode == 0, (
            f"reserved health route unreachable on a repo-less project: "
            f"stdout={probe.stdout.decode()!r} stderr={probe.stderr.decode()!r}"
        )
        assert b'"ok":true' in probe.stdout or b'"ok": true' in probe.stdout, (
            f"health route did not report ok:true: {probe.stdout.decode()!r}"
        )

        # devm status --json reflects the same healthy verdict.
        status = subprocess.run(
            [devm.path, "status", "--json"],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert status.returncode == 0, f"devm status --json failed: {status.stderr.decode()!r}"
        j = json.loads(status.stdout.decode())
        assert j["health"]["proxy_healthy"] is True, (
            f"proxy_healthy not true on a repo-less project: {j['health']}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
