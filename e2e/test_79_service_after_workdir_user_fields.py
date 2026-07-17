"""79: services.X.{after, workdir, user} render into the systemd unit and
take effect at runtime; user: switches the runtime UID.

Fields on the declarative service form:
  - `after: [...]` populates `After=` (systemd start-ordering).
  - `workdir: /path` populates `WorkingDirectory=` (service's cwd).
  - `user: X` populates `User=` (uid the service runs as) — pinned two
    ways: a custom named user (`e2euser`) and the root/default-devm
    UID-0-vs-non-0 contrast.

All are LIVE-bucket per the schema. This test pins the cold-start
happy path for each. Runtime effects are observed via marker files the
services themselves write, so we test what actually happens — not just
what the rendered unit says.

Base image users:
  - devm (uid 1000) — devm's default.
  - root (uid 0) — always available; distinct from devm, so setting
    User=root proves the field wired through.

install: creates a probe user (`e2euser`) so the `probe` service's
assertion doesn't depend on choosing between devm (the default — no
signal) and root (which the base image already privileges for the
provisioner). `asroot`/`asdev` are a separate root-vs-default UID
contrast (test_57).

What this pins:
  - `after` names appear as `After=<name> …` in the rendered unit.
  - `workdir` sets the process cwd (verified via a service that runs
    `pwd`).
  - `user` sets the process uid (verified via `whoami`, and via `id -u`
    for the root/default contrast).
  - `user: "root"` → systemd unit runs as UID 0; no explicit `user:`
    (default "devm") → systemd unit runs as non-root.

What it doesn't cover (tested elsewhere):
  - Ordering enforcement between services declared in `after` — that's
    systemd's job; we trust the parent contract.
  - Nonexistent user rejection — schema validation is out of scope
    here (would be a separate negative test).
  - Systemd service lifecycle (start/stop/restart) -> test_01.
  - Env injection into services -> test_26.
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
            # asroot: write UID, then loop forever.
            "printf '#!/bin/sh\\nid -u > /tmp/uid-as-root\\nexec sleep infinity\\n'"
            " > /tmp/run-asroot.sh && chmod +x /tmp/run-asroot.sh",
            # asdev: same for non-root user.
            "printf '#!/bin/sh\\nid -u > /tmp/uid-as-dev\\nexec sleep infinity\\n'"
            " > /tmp/run-asdev.sh && chmod +x /tmp/run-asdev.sh",
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
            "asroot": {
                "exec": ["/tmp/run-asroot.sh"],
                "user": "root",
                "restart": "always",
            },
            "asdev": {
                "exec": ["/tmp/run-asdev.sh"],
                # No user: field -> defaults to "devm"
                "restart": "always",
            },
        },
    )

    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    tart_sandbox = TartSandbox(name=sandbox_name)

    current = tart_sandbox.state()
    assert current == "running", (
        f"expected VM running after cold-start; got {current!r}"
    )

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

    # root service: must write "0".
    r = tart_sandbox.exec_shell("cat /tmp/uid-as-root")
    assert r.ok, f"uid-as-root marker missing: {r.stderr}"
    uid_root = r.stdout.strip()
    assert uid_root == "0", (
        f"user: 'root' should run as UID 0; got {uid_root!r}"
    )

    # devm service: must write a non-zero UID.
    r = tart_sandbox.exec_shell("cat /tmp/uid-as-dev")
    assert r.ok, f"uid-as-dev marker missing: {r.stderr}"
    uid_dev = r.stdout.strip()
    assert uid_dev != "0", (
        f"default user should run as non-root (devm); got UID {uid_dev!r}"
    )
