"""79: services.X.{after, workdir, user} render into the systemd unit and
take effect at runtime.

Three under-covered fields on the declarative service form:
  - `after: [...]` populates `After=` (systemd start-ordering).
  - `workdir: /path` populates `WorkingDirectory=` (service's cwd).
  - `user: X` populates `User=` (uid the service runs as).

All are LIVE-bucket per the schema. This test pins the cold-start
happy path for each. Runtime effects are observed via a marker file the
service itself writes, so we test what actually happens — not just what
the rendered unit says.

Base image users:
  - admin (uid 1000) — devm's default.
  - root (uid 0) — always available; distinct from admin, so setting
    User=root proves the field wired through.

install: creates a probe user (`e2euser`) so the assertion doesn't
depend on choosing between admin (the default — no signal) and root
(which the base image already privileges for the provisioner).

What this pins:
  - `after` names appear as `After=<name> …` in the rendered unit.
  - `workdir` sets the process cwd (verified via a service that runs
    `pwd`).
  - `user` sets the process uid (verified via `whoami`).

What it doesn't cover (tested elsewhere):
  - Ordering enforcement between services declared in `after` — that's
    systemd's job; we trust the parent contract.
  - Nonexistent user rejection — schema validation is out of scope
    here (would be a separate negative test).
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_after_workdir_user_render_and_take_effect(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        install=[
            # Create a probe user; make /tmp/probe-* pre-writable by them.
            "sudo useradd --create-home --shell /bin/sh e2euser",
            "sudo touch /tmp/probe-whoami /tmp/probe-pwd && "
            "sudo chown e2euser:e2euser /tmp/probe-whoami /tmp/probe-pwd",
        ],
        services={
            "probe": {
                "exec": [
                    "/bin/sh", "-c",
                    "id -un > /tmp/probe-whoami && pwd > /tmp/probe-pwd",
                ],
                "restart": "no",
                "workdir": "/var/tmp",
                "user": "e2euser",
                "after": ["network.target"],
            },
        },
    )

    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    tart_sandbox = TartSandbox(name=sandbox_name)

    # Rendered unit contains After= with our entry (in addition to
    # devm-ready.target which the renderer always prepends).
    r = tart_sandbox.exec_shell("sudo cat /etc/systemd/system/probe.service")
    assert r.ok, f"unit file missing: {r.stderr}"
    unit = r.stdout
    assert "After=devm-ready.target network.target" in unit, (
        f"After= line missing 'network.target':\n{unit}"
    )
    assert "WorkingDirectory=/var/tmp" in unit, (
        f"WorkingDirectory= not rendered:\n{unit}"
    )
    assert "User=e2euser" in unit, (
        f"User= not rendered:\n{unit}"
    )

    # Runtime effect: user field switched uid.
    r = tart_sandbox.exec_shell("cat /tmp/probe-whoami")
    assert r.ok, f"whoami marker missing: {r.stderr}"
    assert r.stdout.strip() == "e2euser", (
        f"service ran as {r.stdout.strip()!r}, expected 'e2euser'"
    )

    # Runtime effect: workdir field set process cwd.
    r = tart_sandbox.exec_shell("cat /tmp/probe-pwd")
    assert r.ok, f"pwd marker missing: {r.stderr}"
    assert r.stdout.strip() == "/var/tmp", (
        f"service ran in {r.stdout.strip()!r}, expected '/var/tmp'"
    )
