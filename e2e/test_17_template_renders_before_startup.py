"""17: template installs BEFORE service startup runs on cold start.

devm's spec.yaml startup ordering is:
  1. cleanup /tmp/.devm-startup
  2. devm-startup.sh (claim volumes, sync /etc/hosts)
  3. install-templates.sh (render+install user templates)
  4. user services' startup commands

Step 3 must complete before step 4 begins — otherwise a service whose
startup command reads its own template file finds nothing there.
test_19 pins post-attach visibility (the file is there by the time
the user shell attaches) but doesn't actually prove the file was
already there at the moment service-startup ran.

This test does prove it: a foreground startup command reads its own
template into a marker file, recording either the rendered content
or a "TEMPLATE_MISSING" sentinel. Post-attach we cat the marker and
verify the rendered content was seen.

What this pins:
  - Template installation completes BEFORE the consuming service's
    startup commands run.
  - The startup command, when it ran, saw the rendered file at the
    declared output path with the substituted values.

What it doesn't cover (tested elsewhere):
  - Template render at all -> test_19.
  - Template LIVE re-render on source edit -> test_20.
"""
from __future__ import annotations

import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm

# The service's startup either records the rendered content into the
# marker, or records TEMPLATE_MISSING. Either way it exits 0 so the
# sandbox brings up cleanly and the test can read the marker after.
STARTUP_PROBE = (
    "if [ -f /etc/probe.conf ]; then "
    "cat /etc/probe.conf > /tmp/startup-saw-template; "
    "else "
    "echo TEMPLATE_MISSING > /tmp/startup-saw-template; "
    "fi"
)


@pytest.mark.timeout(90)
def test_template_renders_before_startup(workspace, devm, sandbox_name):
    tmpl_dir = workspace.path / "configs"
    tmpl_dir.mkdir()
    (tmpl_dir / "probe.conf.tmpl").write_text(
        "PORT={{.Service.probe.Port}}\n"
        "MARKER=ordering-pin-ok\n"
    )

    workspace.write_devmyaml(
        services={
            "probe": {
                "port": 8080,
                "templates": [
                    {"source": "configs/probe.conf.tmpl",
                     "output": "/etc/probe.conf"},
                ],
                "startup": [
                    # Foreground (no background: true) so sbx waits for
                    # this to complete during sandbox bring-up. If the
                    # template hadn't been installed yet, the cat would
                    # find no file and the marker would record
                    # TEMPLATE_MISSING.
                    {"command": ["sh", "-c", STARTUP_PROBE]},
                ],
            },
        },
    )

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)

        # The marker must contain the rendered template content — proving
        # the template was already installed when service-startup ran.
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
