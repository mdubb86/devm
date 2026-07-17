"""11: env + path — injection and LIVE change picked up after reconcile.

Merges what were test_11 (env) and test_35 (path) — both patch a
single LIVE-bucket field, run `devm reconcile --yes`, and assert the
already-attached shell keeps the stale value while a new shell on the
same sandbox picks up the fresh one. Patching BOTH `env:` and `path:`
in one devm.yaml edit and reconciling once proves the same contract
for both fields in a single VM boot.

A project declares a top-level `env:` map and a service with its own
`env:` map, plus (initially absent) a `path:` entry. The first shell
sees the env vars injected: project vars verbatim (`PROJECT_VAR`) and
service vars namespaced and uppercased (`<SERVICE>_LOG_LEVEL`); $PATH
does NOT yet contain the workspace bin dir. devm.yaml is then edited
in one shot to change `LOG_LEVEL` from info to debug AND add a `path:`
entry; `devm reconcile --yes` applies both live changes to the running
sandbox, and a second shell on that same sandbox sees both new values.

Env/path changes on a running VM only take effect via an explicit
`devm reconcile` — nothing fires automatically on `devm shell` attach
(unlike the pre-refactor WriteDevmDir-on-every-shell behavior). This is
the intended "reconcile is where edits go into effect" semantics.

What this pins:
  - Top-level `env:` values are injected into the shell verbatim.
  - Per-service `env:` values are injected as `<SERVICE>_<VAR>` (upper-
    cased service name + var name).
  - `devm reconcile --yes` applies a live env AND path edit to the
    running VM in one call; a NEW shell against that same VM picks up
    both (no recreate required).
  - The FIRST (already-attached) shell keeps the OLD env value AND the
    OLD $PATH — env/path changes are LIVE-bucket but reach the shell
    via with-devm-env sourcing .devm/.env at exec time, so existing
    exec'd shells don't re-source.
  - Second-shell shortcut (attach to running VM) works, and its $PATH
    has the new entry at the head (`.devm/.env`'s PATH form, per
    test_35's original assertion shape).

What it doesn't cover (tested elsewhere):
  - Warm-attach concurrent shells sharing a VM -> test_02.
  - Live port add via reconcile -> test_08.
  - Install-change forcing recreate -> test_14.
  - Cold-start path: -> test_34.
  - path: validation rejection -> schema unit tests.
"""
import time

import pytest

from helpers import Shell

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_env_inject_and_live_change(workspace, devm, tart_sandbox):
    # tart_sandbox fixture already cold-started the VM with minimal config.
    # Write env config; env/path vars are LIVE-bucket, but a running VM
    # only picks up live changes via an explicit reconcile — nothing
    # fires automatically on shell-attach.
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

    expected_head = f"{workspace.path}/bin"

    with Shell(devm, cwd=str(workspace.path)) as first:
        # Bumped to 120 (from 90) — the shell attach races other tests'
        # /vm/start requests to the daemon under pytest-xdist parallelism.
        first.expect_prompt(timeout=120)

        # Both env vars injected. (Service env: `LOG_LEVEL` is exposed as
        # `<SERVICE>_LOG_LEVEL` upper-cased.)
        first.send('echo "GOT_API=$API_LOG_LEVEL GOT_PROJ=$PROJECT_VAR"')
        first.expect_text(r"GOT_API=info GOT_PROJ=projhello", timeout=15)
        first.expect_prompt(timeout=15)

        # First shell: $PATH must NOT yet contain $WORKSPACE/bin.
        first.run_check(
            f"echo $PATH | grep -q '{expected_head}'",
            expect_zero=False, timeout=10,
        )

        # LIVE change: env info -> debug AND add a path: entry, in one
        # devm.yaml edit. Explicit reconcile applies both to the
        # running VM; new shells see the new values.
        workspace.patch_devmyaml(
            env={"PROJECT_VAR": "projhello"},
            services={
                "api": {"port": 8080, "env": {"LOG_LEVEL": "debug"}},
                "worker": {
                    "exec": ["sh", "-c", "while true; do sleep 60; done"],
                    "restart": "always",
                },
            },
            path=["$WORKSPACE/bin"],
        )
        devm.reconcile(yes=True, timeout=60)

        # First shell still sees the OLD values — already-attached
        # shells don't re-source .devm/.env mid-session.
        first.send('echo "STILL_FIRST=$API_LOG_LEVEL"')
        first.expect_text(r"STILL_FIRST=info", timeout=15)
        first.expect_prompt(timeout=15)
        first.run_check(
            f"echo $PATH | grep -q '{expected_head}'",
            expect_zero=False, timeout=10,
        )

        # Second shell on the running VM (shortcut path) sees BOTH new
        # values.
        with Shell(devm, cwd=str(workspace.path)) as second:
            second.expect_prompt(timeout=60)
            second.send('echo "GOT_API=$API_LOG_LEVEL"')
            second.expect_text(r"GOT_API=debug", timeout=15)
            second.expect_prompt(timeout=15)
            second.run_check(
                f"awk -v p=\"$PATH\" -v want='{expected_head}:' "
                "'BEGIN{exit (index(p, want) == 1) ? 0 : 1}'",
                expect_zero=True, timeout=10,
            )
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
