"""07: full happy path — VM running, shell works, worker service up after cold-start.

A project with a long-running background service is cold-started with the
service config already in place. The provisioner deploys the worker service
during the cold-start. The interactive shell reaches a prompt and the
background worker process is verified alive inside the VM.

What this pins:
  - Cold-start brings the VM to 'running' state.
  - Interactive shell reaches a prompt.
  - A service with exec + restart:always leaves a long-lived process
    running inside the VM, detectable via pgrep.
  - devm stop --yes transitions running -> stopped.

What it doesn't cover (tested elsewhere):
  - install: step at cold-create -> test_25.
  - Live port add via reconcile -> test_08.
  - Env injection (project + service vars) -> test_11.
  - Service add/remove churn -> test_21.
  - Port publishing to host -> not pinned here; iron-proxy + daemon own routing.
"""
import subprocess
import time

import pytest

from helpers import Shell
from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_invariant_happy_path(workspace, devm, sandbox_name):
    # Write worker service config BEFORE cold-start so the provisioner
    # deploys it during the first `devm shell` invocation.
    # Use `sleep infinity` (no shell wrapper): exec: joins argv with spaces
    # for ExecStart=, so shell metacharacters in a quoted argument would
    # be mis-parsed by systemd. `sleep infinity` is a single, shell-free token.
    workspace.write_devmyaml(
        services={
            "worker": {
                "exec": ["sleep", "infinity"],
                "restart": "always",
            },
        },
    )

    sandbox = TartSandbox(name=sandbox_name)

    # Cold-start: Shell opens, provisioner runs, worker service is deployed.
    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=120)

        assert sandbox.state() == "running", (
            f"expected VM to be running after cold-start; got {sandbox.state()!r}"
        )

        # Worker daemon is running (sleep infinity stays alive as long as the VM is up).
        r = sandbox.exec_shell(
            "pgrep -af 'sleep infinity' 2>/dev/null | grep -v pgrep | grep -q . && echo OK || echo MISS"
        )
        assert r.ok, f"worker check exec failed: {r.stderr}"
        assert r.stdout.strip() == "OK", f"worker daemon not found: {r.stdout.strip()!r}"

        sh.exit(timeout=30)

    # Anchor-alive: explicitly stop after shell exit.
    devm.stop(yes=True)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sandbox.state() == "stopped":
            return
        time.sleep(0.5)
    pytest.fail("sandbox never reached stopped")
