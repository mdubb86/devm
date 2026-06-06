"""19: template lands at output path on cold start with rendered values.

User declares a `templates:` entry on a service pointing a source file
into a sandbox path. On cold start, the template renders against the
project + service context and lands inside the sandbox before the user
shell attaches — `cat`-able with substituted values.

What this pins:
  - Template output file exists inside the sandbox at the declared path.
  - `{{.Service.<svc>.Port}}` renders to the canonical port.
  - `{{.Service.<svc>.HostPort}}` renders to port_offset + canonical.
  - `{{.Project.ID}}` renders to the workspace slug.
  - Render happens cold (no reconcile invoked).

What it doesn't cover (tested elsewhere):
  - Template LIVE re-render on source edit (test_20).
  - Strict pre-startup ordering vs service entrypoint (not yet pinned —
    test verifies post-attach visibility, not startup-script ordering).
"""
from __future__ import annotations

import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


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
    stop_and_wait_stopped(devm, sandbox_name)
