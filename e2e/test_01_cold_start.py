"""01: cold-start a project (with a declared service) -> VM running -> shell works -> stop.

Consolidated (was also test_07): a project with a long-running
background service is cold-started with the service config already in
place, so the provisioner deploys it during the same cold-start that
proves the ordinary happy path. One boot covers both the generic
lifecycle transitions and the service-deployment invariant.

What this pins:
  - Cold-create path brings the VM to 'running' state.
  - Interactive shell reaches a prompt and can execute a command.
  - A service with exec + restart:always leaves a long-lived process
    running inside the VM, detectable via pgrep (was test_07).
  - Shell exit does NOT auto-stop the VM (the explicit stop-and-verify
    at the end is non-redundant).
  - devm stop --yes transitions running -> stopped within ~15s.

What it doesn't cover (tested elsewhere):
  - Install step executed at cold-create -> test_17c or later.
  - Teardown (sandbox removal) -> test_05.
  - Cold-start with templates -> test_19.
  - Cold-start variants (docker base, curl install) -> test_24, test_25.
  - Live port add via reconcile -> test_08.
  - Env injection (project + service vars) -> test_11.
  - Service add/remove churn -> test_21.
  - Port publishing to host -> not pinned here; iron-proxy + daemon own routing.
"""
from __future__ import annotations

import time

import pytest

from helpers import Shell
from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_cold_start(workspace, devm, sandbox_name):
    # Write worker service config BEFORE cold-start so the provisioner
    # deploys it during the first `devm shell` invocation (was
    # test_07). Use `sleep infinity` (no shell wrapper): exec: joins
    # argv with spaces for ExecStart=, so shell metacharacters in a
    # quoted argument would be mis-parsed by systemd. `sleep infinity`
    # is a single, shell-free token.
    workspace.write_devmyaml(
        services={
            "worker": {
                "exec": ["sleep", "infinity"],
                "restart": "always",
            },
        },
    )

    sandbox = TartSandbox(name=sandbox_name)

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=120)

        current = sandbox.state()
        assert current == "running", (
            f"expected VM to be running after cold-start; got {current!r}"
        )
        # Basic sanity: shell can execute a command.
        sh.run_check("echo hello-from-shell", expect_zero=True, timeout=15)

        # Worker daemon is running (sleep infinity stays alive as long as the VM is up).
        r = sandbox.exec_shell(
            "pgrep -af 'sleep infinity' 2>/dev/null | grep -v pgrep | grep -q . && echo OK || echo MISS"
        )
        assert r.ok, f"worker check exec failed: {r.stderr}"
        assert r.stdout.strip() == "OK", f"worker daemon not found: {r.stdout.strip()!r}"

        sh.exit(timeout=30)

    # Anchor-alive: sandbox stays running after shell exits.
    assert sandbox.state() == "running", (
        "sandbox should still be running after shell exit (anchor-alive)"
    )

    devm.stop(yes=True)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sandbox.state() == "stopped":
            return
        time.sleep(0.5)
    pytest.fail("sandbox never reached stopped")
