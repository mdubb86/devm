"""17: template installs BEFORE service startup runs on cold start.

devm's startup ordering is:
  1. install-templates (render+install user templates)
  2. user services' startup commands

Step 1 must complete before step 2 begins — otherwise a service whose
startup command reads its own template file finds nothing there.

This test proves it: the service's ExecStart reads its own template into
a marker file at startup, recording either the rendered content or a
"TEMPLATE_MISSING" sentinel. Post-attach we cat the marker and verify
the rendered content was seen.

What this pins:
  - Template installation completes BEFORE the consuming service's
    ExecStart runs.
  - The service, when it ran, saw the rendered file at the declared
    output path with the substituted values.

What it doesn't cover (tested elsewhere):
  - Template render at all -> test_19.
  - Template LIVE re-render on source edit -> test_20.
"""
from __future__ import annotations

import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
@pytest.mark.xfail(
    strict=False,
    reason=(
        "devm bug G: template installation is not a provisioner step. "
        "Templates (install-templates.sh) are only applied via ApplyLive "
        "(reconcile), not at cold-start. The probe service sees "
        "TEMPLATE_MISSING instead of the rendered /etc/probe.conf. "
        "Remove xfail when bug G lands."
    ),
)
def test_template_renders_before_startup(workspace, devm, sandbox_name):
    tmpl_dir = workspace.path / "configs"
    tmpl_dir.mkdir()
    (tmpl_dir / "probe.conf.tmpl").write_text(
        "PORT={{.Service.probe.Port}}\n"
        "MARKER=ordering-pin-ok\n"
    )

    # Pre-write the probe script via install: to avoid shell metacharacters
    # in ExecStart= (exec: joins argv with spaces without quoting, so
    # ["sh", "-c", "<complex-script>"] would be mis-parsed by systemd).
    # The probe records the template content (or TEMPLATE_MISSING) then
    # execs sleep infinity so the provisioner health poll sees "active".
    workspace.write_devmyaml(
        install=[
            "printf '#!/bin/sh\\n"
            "if [ -f /etc/probe.conf ]; then\\n"
            "  cat /etc/probe.conf > /tmp/startup-saw-template\\n"
            "else\\n"
            "  echo TEMPLATE_MISSING > /tmp/startup-saw-template\\n"
            "fi\\n"
            "exec sleep infinity\\n"
            "' > /tmp/probe.sh && chmod +x /tmp/probe.sh",
        ],
        services={
            "probe": {
                "port": 8080,
                "templates": [
                    {"source": "configs/probe.conf.tmpl",
                     "output": "/etc/probe.conf"},
                ],
                # exec: single-element argv so ExecStart= is just
                # /tmp/probe.sh — no quoting needed.
                "exec": ["/tmp/probe.sh"],
                "restart": "always",
            },
        },
    )

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=120)

        # The marker must contain the rendered template content — proving
        # the template was already installed when service startup ran.
        sh.send("cat /tmp/startup-saw-template")
        sh.expect_text(r"PORT=8080", timeout=10)
        sh.expect_text(r"MARKER=ordering-pin-ok", timeout=10)
        sh.expect_prompt(timeout=10)

        # Belt: explicitly verify the negative sentinel is NOT present.
        sh.run_check(
            "grep -q TEMPLATE_MISSING /tmp/startup-saw-template",
            expect_zero=False, timeout=10,
        )

        sh.exit(timeout=30)

    stop_and_wait_stopped(devm, sandbox_name)
