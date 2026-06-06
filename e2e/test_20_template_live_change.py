"""20: editing a template source + reconcile re-renders LIVE (no recreate).

User edits the template source file on the host then runs reconcile.
The output file inside the sandbox reflects the new content, the user
shell survives (proving LIVE bucket, not teardown/recreate), and devm
reports the template diff line on stdout.

What this pins:
  - Template source edit is classified into the LIVE bucket.
  - Reconcile stdout includes `~ template: <svc> -> <output>`.
  - The pre-existing user shell remains alive across the reconcile.
  - The rendered file inside the sandbox shows the new content.

What it doesn't cover (tested elsewhere):
  - Cold-start template render (test_19).
  - Reconcile prompt+yes mechanics under recreate (test_09).
  - Other LIVE-bucket diffs: ports (test_08, test_12), env (test_11),
    networks (test_13).
"""
from __future__ import annotations
import subprocess

import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(60)
def test_template_live_change(workspace, devm, sandbox_name):
    tmpl_dir = workspace.path / "configs"
    tmpl_dir.mkdir()
    tmpl_path = tmpl_dir / "msg.tmpl"
    tmpl_path.write_text("first {{.Project.ID}}\n")

    workspace.write_devmyaml(
        services={
            "probe": {
                "port": 8080,
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

    # Anchor-alive: explicitly stop after shell exit.
    stop_and_wait_stopped(devm, sandbox_name)
