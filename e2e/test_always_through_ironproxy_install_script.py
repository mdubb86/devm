"""Pin: iron-proxy is in the path for every guest HTTPS request from
begin-provisioning onward. An `install:` script that hits an external
HTTPS host appears in iron-proxy's audit log.

Under v0.21.0, install: ran under OPEN egress — softnet routed direct
to real IPs, iron-proxy was bypassed, install: hits were absent from
the audit log. This test is the strongest possible proof of the new
invariant: audit-log presence.
"""
from __future__ import annotations

import json
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_install_script_hits_iron_proxy(devm, workspace):
    workspace.write_devmyaml(
        install=["curl -fsSL https://astral.sh/uv/install.sh -o /tmp/u.sh"],
        network={"allow": ["astral.sh", "deb.debian.org", "security.debian.org"]},
    )
    try:
        r = subprocess.run(
            [devm.path, "start"], cwd=str(workspace.path),
            capture_output=True, timeout=240,
        )
        assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

        proxy_log_lines = workspace.read_proxy_log()
        hit_astral = False
        for line in proxy_log_lines:
            try:
                entry = json.loads(line)
            except json.JSONDecodeError:
                continue
            audit = entry.get("audit") or {}
            if audit.get("host") == "astral.sh":
                hit_astral = True
                break
        assert hit_astral, (
            "install: hit to astral.sh was not audited by iron-proxy — "
            "the always-through invariant is not in effect.\n"
            "Log tail:\n" + "".join(proxy_log_lines[-30:])
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
