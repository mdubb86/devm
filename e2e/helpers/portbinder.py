"""Portbinder helper availability check for standalone tests.

The B3 per-project-bind-isolation e2e tests (test_portbinder_contract,
test_per_project_ip_concurrent/_release/_adopt, test_ssh_config_hostname,
test_loopback_contract) all assume the real root `devm-portbinder`
LaunchDaemon + its lo0-alias-provisioned 127.42/16 pool are already
present on this machine (`devm install`). They're standalones, not part
of the default isolated e2e lane (E2E_ISOLATE=1, no sudo, no helper) —
this is the shared skip guard every one of them uses so they skip
cleanly instead of failing when the helper hasn't been installed.
"""
from __future__ import annotations

from pathlib import Path

PORTBINDER_SOCKET = Path("/var/run/devm-portbinder.sock")


def helper_installed() -> bool:
    return PORTBINDER_SOCKET.exists()
