"""20: editing a template source + reconcile = LIVE update (no recreate).

The user shell survives, the output file inside the sandbox shows the
new rendered content, and devm reports `~ template: <svc> → <output>`.
"""
from __future__ import annotations
import subprocess
import time

import pytest

from helpers import Shell, sbx


@pytest.mark.timeout(60)
def test_template_live_change(workspace, devm, sandbox_name):
    tmpl_dir = workspace.path / "configs"
    tmpl_dir.mkdir()
    tmpl_path = tmpl_dir / "msg.tmpl"
    tmpl_path.write_text("first {{.Project.ID}}\n")

    workspace.write_devmyaml(
        services={
            "probe": {
                "canonical": 8080,
                "templates": [
                    {"source": "configs/msg.tmpl", "output": "/etc/msg"},
                ],
            },
        },
    )

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)
        # Baseline content.
        sh.send("cat /etc/msg")
        sh.expect_text(rf"first {workspace.slug}", timeout=10)
        sh.expect_prompt(timeout=10)

        # Mutate the source on the host.
        tmpl_path.write_text("second {{.Project.ID}}\n")

        # Reconcile from outside the shell.
        result = devm.reconcile(yes=True, timeout=60)
        assert b"~ template: probe \xe2\x86\x92 /etc/msg" in result.stdout or \
               "~ template: probe → /etc/msg" in result.stdout.decode(), \
               f"reconcile stdout did not show template change: {result.stdout.decode()!r}"

        # The user shell MUST still be alive (LIVE — no recreate).
        sh.run_check("echo still-here", expect_zero=True, timeout=10)

        # New content reflected.
        sh.send("cat /etc/msg")
        sh.expect_text(rf"second {workspace.slug}", timeout=10)
        sh.expect_prompt(timeout=10)

        sh.exit(timeout=30)

    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "stopped":
            return
        time.sleep(0.5)
    pytest.fail(f"sandbox {sandbox_name} never reached 'stopped'")
