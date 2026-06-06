"""07: full happy path — install ran, canonical port published, worker daemon up.

A project with an install: step, a service that declares a canonical
port, and a service with a background startup command is cold-started
via `devm shell`. The interactive shell reaches a prompt, the install
marker is present, the canonical port is mapped to the expected host
port (port_offset + canonical), and the worker's background process is
alive inside the sandbox.

What this pins:
  - install: step ran at cold-create (marker file present).
  - Canonical service port 8080 is published to host port
    port_offset + 8080 (canonical-port mapping invariant).
  - A service `startup:` entry with `background: True` actually leaves
    a long-lived process running inside the sandbox after cold-start,
    detected via pgrep with a self-match filter.
  - Interactive shell reaches a prompt and survives until exit.

What it doesn't cover (tested elsewhere):
  - Cold-start basic (install + shell + stop) -> test_01.
  - Live port add via reconcile -> test_08.
  - Env injection (project + service vars) -> test_11.
  - Service add/remove churn -> test_21.
"""
import subprocess

import pytest

from helpers import Shell, sbx, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(60)
def test_invariant_happy_path(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        install=["touch /tmp/install-marker"],
        services={
            "api": {"port": 8080},
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

        # Worker daemon is running. Filter the pgrep self-match: the
        # sh process running `pgrep -af MARKER` has MARKER in its own
        # argv and would otherwise return a false positive. `grep -v
        # pgrep` drops that line. Without this filter the assertion
        # passes regardless of whether a real daemon is alive.
        out = subprocess.run(
            ["sbx", "exec", sandbox_name, "sh", "-c",
             "pgrep -af 'while true.*sleep 60' 2>/dev/null | grep -v pgrep | grep -q . && echo OK || echo MISS"],
            capture_output=True, timeout=15, check=True,
        ).stdout.decode().strip()
        assert out == "OK", f"worker daemon not found: {out!r}"

        sh.exit(timeout=30)

    # Anchor-alive: explicitly stop after shell exit.
    stop_and_wait_stopped(devm, sandbox_name)
