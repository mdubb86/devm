"""A service's `exec:` scalar (function-ref form) renders a wrapper
script and points ExecStart= at it; an `exec:` sequence (argv form)
renders ExecStart= directly with no wrapper. Both shapes come up
active. Unit files land at /etc/systemd/system/<service-name>.service
(internal/scripts/install.sh.tmpl installs by basename — no `devm-`
prefix on the service name itself).
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_service_exec_string_form_wraps_argv_form_does_not(workspace, devm):
    workspace.write_devmyaml(
        no_repo=True,
        services={
            "a": {"exec": "run-a"},
            "b": {"exec": ["/bin/sleep", "infinity"]},
        },
    )
    workspace.write_devm_sh(
        "#!/usr/bin/env bash\n"
        "set -eo pipefail\n"
        "run-a() {\n"
        "  exec sleep infinity\n"
        "}\n"
    )
    try:
        cold = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=240,
        )
        assert cold.returncode == 0, f"start failed: {cold.stderr.decode()!r}"

        cat_a = subprocess.run(
            [devm.path, "exec", "systemctl", "cat", "a.service"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert cat_a.returncode == 0, cat_a.stderr.decode()
        assert "ExecStart=/opt/devm/service-wrappers/a.sh" in cat_a.stdout.decode()

        cat_b = subprocess.run(
            [devm.path, "exec", "systemctl", "cat", "b.service"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert cat_b.returncode == 0, cat_b.stderr.decode()
        assert "ExecStart=/bin/sleep infinity" in cat_b.stdout.decode()

        for name in ("a", "b"):
            status = subprocess.run(
                [devm.path, "exec", "systemctl", "status", f"{name}.service", "--no-pager"],
                cwd=str(workspace.path), capture_output=True, timeout=30,
            )
            assert "active (running)" in status.stdout.decode(), (
                f"{name}.service not active: {status.stdout.decode()!r}"
            )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
