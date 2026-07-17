"""19: templates — cold render, startup ordering, sudo:false path, live reconcile.

User declares `templates:` entries on services pointing source files
into sandbox paths. Consolidated end-to-end proof, one cold-start VM:
  - Cold render + value substitution (`Service.Port`, `Service.HostPort`,
    `Project.Name`) at a `sudo:true` /etc path.
  - Template installation completes BEFORE the consuming service's
    ExecStart runs (a service that reads its own template at startup
    sees the rendered file, not a "missing" sentinel).
  - Default `sudo:false` renders to a guest-user-writable path
    (/home/devm/...), lands devm-owned (not root), and the guest user
    can rewrite it without sudo.
  - Editing a template source + `devm reconcile` re-renders LIVE (no
    recreate): the pre-existing shell survives, reconcile stdout shows
    the template diff line, and the sandbox file reflects new content.

Four independent services (one per concern, distinct ports/outputs so
none collide) share the single boot — each assertion is checkable
right after the first `expect_prompt()`, except the live-reconcile
phase which runs last (it mutates state).

What this pins (superset of test_17/19/20/80):
  - Template output file exists inside the sandbox at the declared
    path, with correct rendered values, on cold start.
  - Render happens cold (no reconcile invoked) for the base case.
  - Template install precedes service startup (ordering pin).
  - `sudo:false` writes to a guest-writable path, devm-owned, content
    rendered (not verbatim).
  - Template source edit is classified into the LIVE bucket; reconcile
    stdout includes `~ template: <svc> -> <output>`; the shell stays
    alive; the rendered file shows the new content.

What it doesn't cover (tested elsewhere):
  - Reconcile prompt+yes mechanics under recreate (test_09).
  - Other LIVE-bucket diffs: ports (test_08, test_12), env (test_11),
    networks (test_13).
"""
from __future__ import annotations

import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(240)
def test_templates_cold_start_ordering_sudo_and_live_reconcile(
    workspace, devm, sandbox_name
):
    tmpl_dir = workspace.path / "configs"
    tmpl_dir.mkdir()

    # test_19: cold render + value substitution at sudo:true /etc path.
    (tmpl_dir / "cold.conf.tmpl").write_text(
        "PORT={{.Service.coldsvc.Port}}\n"
        "HOSTPORT={{.Service.coldsvc.HostPort}}\n"
        "PROJECT={{.Project.Name}}\n"
    )

    # test_17: ordering pin — this service's own ExecStart reads its own
    # template into a marker file, recording either the rendered content
    # or a "TEMPLATE_MISSING" sentinel.
    (tmpl_dir / "order.conf.tmpl").write_text(
        "PORT={{.Service.orderingsvc.Port}}\n"
        "MARKER=ordering-pin-ok\n"
    )

    # test_20: baseline content, mutated + reconciled later in this test.
    live_tmpl_path = tmpl_dir / "msg.tmpl"
    live_tmpl_path.write_text("first {{.Project.Name}}\n")

    # test_80: default sudo:false, guest-user-writable path.
    (tmpl_dir / "user.conf.tmpl").write_text(
        "PROJECT={{.Project.Name}}\n"
        "PORT={{.Service.userpathsvc.Port}}\n"
    )

    workspace.write_devmyaml(
        # Pre-write the ordering probe script via install: to avoid shell
        # metacharacters in ExecStart= (exec: joins argv with spaces
        # without quoting, so a complex inline script would be
        # mis-parsed by systemd). Records template content (or
        # TEMPLATE_MISSING) then execs sleep infinity so the health
        # poll sees "active".
        install=[
            "printf '#!/bin/sh\\n"
            "if [ -f /etc/order.conf ]; then\\n"
            "  cat /etc/order.conf > /tmp/startup-saw-template\\n"
            "else\\n"
            "  echo TEMPLATE_MISSING > /tmp/startup-saw-template\\n"
            "fi\\n"
            "exec sleep infinity\\n"
            "' > /tmp/probe.sh && chmod +x /tmp/probe.sh",
        ],
        services={
            "coldsvc": {
                "port": 8080,
                "templates": [
                    {"source": "configs/cold.conf.tmpl",
                     "output": "/etc/cold.conf",
                     "sudo": True},
                ],
            },
            "orderingsvc": {
                "port": 8081,
                "templates": [
                    {"source": "configs/order.conf.tmpl",
                     "output": "/etc/order.conf",
                     "sudo": True},
                ],
                # exec: single-element argv so ExecStart= is just
                # /tmp/probe.sh — no quoting needed.
                "exec": ["/tmp/probe.sh"],
                "restart": "always",
            },
            "livesvc": {
                "port": 8082,
                "templates": [
                    {"source": "configs/msg.tmpl",
                     "output": "/etc/msg",
                     "sudo": True},
                ],
            },
            "userpathsvc": {
                "port": 9090,
                "templates": [
                    {
                        "source": "configs/user.conf.tmpl",
                        "output": "/home/devm/user.conf",
                        # sudo omitted — pin defaults to false.
                    },
                ],
            },
        },
    )

    # Owns cold-start: the Shell context is the first `devm shell` — it
    # triggers cold-start with the yaml already in place, so the
    # provisioner's install-templates step sees all four templates.
    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=120)

        # ---- test_19: cold render + value substitution. ----
        sh.run_check("test -f /etc/cold.conf", expect_zero=True, timeout=10)
        sh.send("cat /etc/cold.conf")
        sh.expect_text(r"PORT=8080", timeout=10)
        sh.expect_text(r"HOSTPORT=8080", timeout=10)
        sh.expect_text(rf"PROJECT={workspace.slug}", timeout=10)
        sh.expect_prompt(timeout=10)

        # ---- test_17: template installed BEFORE service startup ran. ----
        sh.send("cat /tmp/startup-saw-template")
        sh.expect_text(r"PORT=8081", timeout=10)
        sh.expect_text(r"MARKER=ordering-pin-ok", timeout=10)
        sh.expect_prompt(timeout=10)
        # Belt: explicitly verify the negative sentinel is NOT present.
        sh.run_check(
            "grep -q TEMPLATE_MISSING /tmp/startup-saw-template",
            expect_zero=False, timeout=10,
        )

        # ---- test_80: sudo:false lands guest-writable, devm-owned. ----
        sh.run_check("test -f /home/devm/user.conf", expect_zero=True, timeout=10)
        # Emit a distinctive marker so the assertion doesn't collide with
        # the echoed command line.
        sh.send("printf 'DEVM_OWNER=%s\\n' \"$(stat -c '%U' /home/devm/user.conf)\"")
        sh.expect_text(r"DEVM_OWNER=devm", timeout=10)
        sh.expect_prompt(timeout=10)
        sh.send("cat /home/devm/user.conf")
        sh.expect_text(rf"PROJECT={workspace.slug}", timeout=10)
        sh.expect_text(r"PORT=9090", timeout=10)
        sh.expect_prompt(timeout=10)
        # As devm, we can rewrite the file — no sudo needed.
        sh.run_check("echo overwritten > /home/devm/user.conf",
                     expect_zero=True, timeout=10)

        # ---- test_20: live re-render on source edit + reconcile. ----
        # Baseline content.
        sh.send("cat /etc/msg")
        sh.expect_text(rf"first {workspace.slug}", timeout=10)
        sh.expect_prompt(timeout=10)

        # Mutate the source on the host.
        live_tmpl_path.write_text("second {{.Project.Name}}\n")

        # Reconcile from outside the shell.
        result = devm.reconcile(yes=True, timeout=60)
        assert b"~ template: livesvc \xe2\x86\x92 /etc/msg" in result.stdout or \
               "~ template: livesvc → /etc/msg" in result.stdout.decode(), \
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
