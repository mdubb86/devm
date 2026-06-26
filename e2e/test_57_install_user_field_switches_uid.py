"""57: service user: field switches the systemd unit's User= and runtime UID.

A service with `user: "root"` runs as UID 0. A service with no explicit
user (defaults to "dev") runs as the "dev" user (non-root). Each writes
`id -u` to a marker file; after cold-start the markers must contain the
expected UIDs.

In Ship 4, services are rendered as systemd units. The `user:` field in
devm.yaml maps directly to `User=` in the [Service] section. The default
user is "dev" (see internal/render/systemd.go).

What this pins:
  - `user: "root"` → systemd unit runs as UID 0.
  - No explicit user (default "dev") → systemd unit runs as non-root.

What it doesn't cover (tested elsewhere):
  - Systemd service lifecycle (start/stop/restart) -> test_07.
  - Env injection into services -> test_60.
"""
from __future__ import annotations

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_user_field_switches_uid(workspace, devm, tart_sandbox):
    workspace.write_devmyaml(
        services={
            "asroot": {
                "exec": ["sh", "-c", "id -u > /tmp/uid-as-root"],
                "user": "root",
                "restart": "no",
            },
            "asdev": {
                "exec": ["sh", "-c", "id -u > /tmp/uid-as-dev"],
                # No user: field -> defaults to "dev"
                "restart": "no",
            },
        },
    )

    assert tart_sandbox.state() == "running", (
        f"expected VM running; got {tart_sandbox.state()!r}"
    )

    # root service: must write "0".
    r = tart_sandbox.exec_shell("cat /tmp/uid-as-root")
    assert r.ok, f"uid-as-root marker missing: {r.stderr}"
    uid_root = r.stdout.strip()
    assert uid_root == "0", (
        f"user: 'root' should run as UID 0; got {uid_root!r}"
    )

    # dev service: must write a non-zero UID.
    r = tart_sandbox.exec_shell("cat /tmp/uid-as-dev")
    assert r.ok, f"uid-as-dev marker missing: {r.stderr}"
    uid_dev = r.stdout.strip()
    assert uid_dev != "0", (
        f"default user should run as non-root (dev); got UID {uid_dev!r}"
    )
