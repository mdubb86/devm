"""57: service user: field switches the systemd unit's User= and runtime UID.

A service with `user: "root"` runs as UID 0. A service with no explicit
user (defaults to "devm") runs as the "devm" user (non-root). Each writes
`id -u` to a marker file; after cold-start the markers must contain the
expected UIDs.

Services are rendered as systemd units. The `user:` field in devm.yaml
maps directly to `User=` in the [Service] section. The default user is
"devm" (see internal/render/systemd.go).

Helper scripts are pre-written via install: and exec'd as single-argument
execs (no shell wrapper). This avoids systemd's ExecStart= quoting
limitations — exec: joins argv with spaces and doesn't quote elements that
contain shell metacharacters; scripts in /tmp sidestep the issue entirely.

What this pins:
  - `user: "root"` → systemd unit runs as UID 0.
  - No explicit user (default "devm") → systemd unit runs as non-root.

What it doesn't cover (tested elsewhere):
  - Systemd service lifecycle (start/stop/restart) -> test_07.
  - Env injection into services -> test_60.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_user_field_switches_uid(workspace, devm, sandbox_name):
    # Pre-write helper scripts via install: so services can exec them
    # without shell metacharacters in ExecStart=. install: runs as devm
    # before services start, so scripts exist by the time services exec them.
    # restart:always: service stays active so provisioner health poll passes.
    workspace.write_devmyaml(
        install=[
            # asroot: write UID, then loop forever.
            "printf '#!/bin/sh\\nid -u > /tmp/uid-as-root\\nexec sleep infinity\\n'"
            " > /tmp/run-asroot.sh && chmod +x /tmp/run-asroot.sh",
            # asdev: same for non-root user.
            "printf '#!/bin/sh\\nid -u > /tmp/uid-as-dev\\nexec sleep infinity\\n'"
            " > /tmp/run-asdev.sh && chmod +x /tmp/run-asdev.sh",
        ],
        services={
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

    sandbox = TartSandbox(name=sandbox_name)

    # Cold-start: provisions and starts both services.
    subprocess.run(
        [devm.path, "shell", "--", "true"],
        capture_output=True, cwd=str(workspace.path),
        timeout=300, check=False,
    )

    assert sandbox.state() == "running", (
        f"expected VM running after cold-start; got {sandbox.state()!r}"
    )

    # root service: must write "0".
    r = sandbox.exec_shell("cat /tmp/uid-as-root")
    assert r.ok, f"uid-as-root marker missing: {r.stderr}"
    uid_root = r.stdout.strip()
    assert uid_root == "0", (
        f"user: 'root' should run as UID 0; got {uid_root!r}"
    )

    # devm service: must write a non-zero UID.
    r = sandbox.exec_shell("cat /tmp/uid-as-dev")
    assert r.ok, f"uid-as-dev marker missing: {r.stderr}"
    uid_dev = r.stdout.strip()
    assert uid_dev != "0", (
        f"default user should run as non-root (devm); got UID {uid_dev!r}"
    )
