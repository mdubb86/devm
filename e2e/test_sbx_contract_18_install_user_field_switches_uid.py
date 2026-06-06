"""install/startup: user: field switches the runtime UID.

Two startup steps: one with user: "0" (root), one with user: "1000"
(agent). Each writes `whoami` to a marker file. After bringup, the
markers must contain "root" and "agent" respectively.

Devm dependency: internal/render/spec.go uses `user: "1000"` for
init-volumes (so chown -R agent:agent works without sudo) and
`user: "0"` for install-templates (so it can write to /etc, /usr).
If user: changed semantics, both scripts silently break in different
ways.
"""
from __future__ import annotations

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(120)
def test_user_field_switches_runtime_uid(sandbox_name):
    startup = [
        {
            "command": ["sh", "-c", "whoami > /tmp/who-as-root"],
            "user": "0",
            "description": "I3 as root",
        },
        {
            "command": ["sh", "-c", "whoami > /tmp/who-as-agent"],
            "user": "1000",
            "description": "I3 as agent",
        },
    ]
    with contract_sandbox(minimal_kit(startup=startup), sandbox_name):
        as_root = sbx_exec(sandbox_name, "cat", "/tmp/who-as-root")
        as_agent = sbx_exec(sandbox_name, "cat", "/tmp/who-as-agent")
        assert as_root.returncode == 0, f"root marker missing: {as_root.stderr.decode()}"
        assert as_agent.returncode == 0, f"agent marker missing: {as_agent.stderr.decode()}"
        assert as_root.stdout.decode().strip() == "root", (
            f"user: '0' should run as root; got {as_root.stdout.decode()!r}"
        )
        assert as_agent.stdout.decode().strip() == "agent", (
            f"user: '1000' should run as agent; got {as_agent.stdout.decode()!r}"
        )
