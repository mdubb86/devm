"""11: env injection — project + service vars; LIVE change picked up after reconcile.

A project declares a top-level `env:` map and a service with its own
`env:` map. The first shell sees both injected: project vars verbatim
(`PROJECT_VAR`) and service vars namespaced and uppercased
(`<SERVICE>_LOG_LEVEL`). devm.yaml is then edited to change `LOG_LEVEL`
from info to debug; `devm reconcile --yes` applies the live change to
the running sandbox, and a second shell on that same sandbox sees the
new value.

Env/path changes on a running VM only take effect via an explicit
`devm reconcile` — nothing fires automatically on `devm shell` attach
(unlike the pre-refactor WriteDevmDir-on-every-shell behavior). This is
the intended "reconcile is where edits go into effect" semantics.

What this pins:
  - Top-level `env:` values are injected into the shell verbatim.
  - Per-service `env:` values are injected as `<SERVICE>_<VAR>` (upper-
    cased service name + var name).
  - `devm reconcile --yes` applies a live env edit to the running VM;
    a NEW shell against that same VM picks it up (no recreate
    required).
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
    # Write env config; env vars are LIVE-bucket, but a running VM only
    # picks up live changes via an explicit reconcile — nothing fires
    # automatically on shell-attach.
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
    devm.reconcile(yes=True, timeout=60)

    with Shell(devm, cwd=str(workspace.path)) as first:
        # Bumped to 120 (from 90) — the shell attach races other tests'
        # /vm/start requests to the daemon under pytest-xdist parallelism.
        first.expect_prompt(timeout=120)

        # Both env vars injected. (Service env: `LOG_LEVEL` is exposed as
        # `<SERVICE>_LOG_LEVEL` upper-cased.)
        first.send('echo "GOT_API=$API_LOG_LEVEL GOT_PROJ=$PROJECT_VAR"')
        first.expect_text(r"GOT_API=info GOT_PROJ=projhello", timeout=15)
        first.expect_prompt(timeout=15)

        # LIVE change: info -> debug. Explicit reconcile applies it to the
        # running VM; new shells see the new value.
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
        devm.reconcile(yes=True, timeout=60)

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
