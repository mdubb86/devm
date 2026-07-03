"""11: env injection — project + service vars; LIVE change picked up by next shell.

A project declares a top-level `env:` map and a service with its own
`env:` map. The first shell sees both injected: project vars verbatim
(`PROJECT_VAR`) and service vars namespaced and uppercased
(`<SERVICE>_LOG_LEVEL`). devm.yaml is then edited live to change
`LOG_LEVEL` from info to debug; a second shell on the same running
sandbox sees the new value, confirming live env change is picked up at
shell-attach time.

What this pins:
  - Top-level `env:` values are injected into the shell verbatim.
  - Per-service `env:` values are injected as `<SERVICE>_<VAR>` (upper-
    cased service name + var name).
  - A live edit to a service env value is picked up by a NEW shell
    against the same running VM (no recreate required).
  - The FIRST (already-attached) shell keeps the OLD value — env
    changes are LIVE-bucket but reach the env via with-devm-env
    sourcing .devm/.env at exec time, so existing exec'd shells don't
    re-source. (Same contract pinned for path: by test_35.)
  - Second-shell shortcut (attach to running VM) works.

What it doesn't cover (tested elsewhere):
  - Warm-attach concurrent shells sharing a VM -> test_02.
  - Live port add via reconcile -> test_08.
  - Install-change forcing recreate -> test_14.
"""
import time

import pytest

from helpers import Shell

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_env_inject_and_live_change(workspace, devm, tart_sandbox):
    # tart_sandbox fixture already cold-started the VM with minimal config.
    # Write env config; env vars are LIVE-bucket so the warm-attach shell picks them up.
    workspace.write_devmyaml(
        env={"PROJECT_VAR": "projhello"},
        services={
            "api": {"port": 8080, "env": {"LOG_LEVEL": "info"}},
            "worker": {
                "exec": ["sh", "-c", "while true; do sleep 60; done"],
                "restart": "always",
            },
        },
    )

    with Shell(devm, cwd=str(workspace.path)) as first:
        # Bumped to 120 (from 90) — the shell attach races other tests'
        # /vm/start requests to the daemon under pytest-xdist parallelism.
        first.expect_prompt(timeout=120)

        # Both env vars injected. (Service env: `LOG_LEVEL` is exposed as
        # `<SERVICE>_LOG_LEVEL` upper-cased.)
        first.send('echo "GOT_API=$API_LOG_LEVEL GOT_PROJ=$PROJECT_VAR"')
        first.expect_text(r"GOT_API=info GOT_PROJ=projhello", timeout=15)
        first.expect_prompt(timeout=15)

        # LIVE change: info -> debug. New shells see the new value.
        workspace.patch_devmyaml(
            env={"PROJECT_VAR": "projhello"},
            services={
                "api": {"port": 8080, "env": {"LOG_LEVEL": "debug"}},
                "worker": {
                    "exec": ["sh", "-c", "while true; do sleep 60; done"],
                    "restart": "always",
                },
            },
        )

        # First shell still sees the OLD value — already-attached
        # shells don't re-source .devm/.env mid-session.
        first.send('echo "STILL_FIRST=$API_LOG_LEVEL"')
        first.expect_text(r"STILL_FIRST=info", timeout=15)
        first.expect_prompt(timeout=15)

        # Second shell on the running VM (shortcut path).
        with Shell(devm, cwd=str(workspace.path)) as second:
            second.expect_prompt(timeout=60)
            second.send('echo "GOT_API=$API_LOG_LEVEL"')
            second.expect_text(r"GOT_API=debug", timeout=15)
            second.expect_prompt(timeout=15)
            second.exit(timeout=30)

        first.exit(timeout=30)

    # Anchor-alive: explicitly stop after shell exits.
    devm.stop(yes=True)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if tart_sandbox.state() == "stopped":
            return
        time.sleep(0.5)
    pytest.fail("sandbox never reached stopped")
