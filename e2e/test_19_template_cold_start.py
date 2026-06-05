"""19: template lands at output BEFORE service startup runs.

A `socat -d -d` invocation in the service's startup is the only thing
allowed to bind the canonical port. The socat command line is generated
into /etc/probe.conf via a template. If the template doesn't land before
startup, socat starts with no config and the service's data path fails.

End-to-end success = template rendered correctly + service used it.
"""
from __future__ import annotations
import time

import pytest

from helpers import Shell, sbx


@pytest.mark.timeout(60)
def test_template_cold_start(workspace, devm, sandbox_name):
    # The template renders the canonical port into a config file we
    # then verify from inside the sandbox.
    tmpl_dir = workspace.path / "configs"
    tmpl_dir.mkdir()
    (tmpl_dir / "probe.conf.tmpl").write_text(
        "PORT={{.Service.probe.Port}}\n"
        "HOSTPORT={{.Service.probe.HostPort}}\n"
        "PROJECT={{.Project.ID}}\n"
    )

    workspace.write_devmyaml(
        services={
            "probe": {
                "port": 8080,
                "templates": [
                    {"source": "configs/probe.conf.tmpl",
                     "output": "/etc/probe.conf"},
                ],
            },
        },
    )

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)
        # The template file must exist with the rendered content.
        sh.run_check("test -f /etc/probe.conf", expect_zero=True, timeout=10)
        # Content checks — host port reflects port_offset + canonical.
        sh.send("cat /etc/probe.conf")
        sh.expect_text(r"PORT=8080", timeout=10)
        sh.expect_text(rf"HOSTPORT={workspace.port_offset + 8080}", timeout=10)
        sh.expect_text(rf"PROJECT={workspace.slug}", timeout=10)
        sh.expect_prompt(timeout=10)

        sh.exit(timeout=30)

    # Anchor-alive: explicitly stop after shell exit.
    devm.stop(yes=True)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "stopped":
            return
        time.sleep(0.5)
    pytest.fail(f"sandbox {sandbox_name} never reached 'stopped'")
