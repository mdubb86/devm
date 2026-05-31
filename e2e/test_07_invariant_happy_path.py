"""07: full happy path — install ran, canonical port published, worker daemon up."""
import subprocess
import time

import pytest

from helpers import Shell, sbx


@pytest.mark.timeout(60)
def test_invariant_happy_path(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        install=["touch /tmp/install-marker"],
        services={
            "api": {"canonical": 8080},
            "worker": {
                "startup": [
                    {"command": ["sh", "-c", "while true; do sleep 60; done"],
                     "background": True}
                ],
            },
        },
    )

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)

        # install marker present (proves install ran)
        sh.run_check("test -e /tmp/install-marker", expect_zero=True, timeout=30)

        # Canonical port 8080 mapped to host port (port_offset + 8080).
        sbx.wait_for_port_published(
            sandbox_name, sandbox_port=8080,
            host_port=workspace.port_offset + 8080, timeout=30,
        )

        # Worker daemon is running.
        out = subprocess.run(
            ["sbx", "exec", sandbox_name, "sh", "-c",
             "pgrep -f 'while true; do sleep 60' >/dev/null && echo OK || echo MISS"],
            capture_output=True, timeout=15, check=True,
        ).stdout.decode().strip()
        assert out == "OK", f"worker daemon not found: {out!r}"

        sh.exit(timeout=30)

    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "stopped":
            return
        time.sleep(0.5)
    pytest.fail(f"sandbox {sandbox_name} never reached 'stopped'")
