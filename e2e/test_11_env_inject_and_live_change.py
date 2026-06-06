"""11: env injection — project + service vars; LIVE change picked up by next shell."""
import time

import pytest

from helpers import Shell, sbx

pytestmark = pytest.mark.devm


@pytest.mark.timeout(75)
def test_env_inject_and_live_change(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        env={"PROJECT_VAR": "projhello"},
        services={
            "api": {"port": 8080, "env": {"LOG_LEVEL": "info"}},
            "worker": {
                "startup": [
                    {"command": ["sh", "-c", "while true; do sleep 60; done"],
                     "background": True}
                ],
            },
        },
    )

    with Shell(devm, cwd=str(workspace.path)) as first:
        first.expect_prompt(timeout=90)

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
                    "startup": [
                        {"command": ["sh", "-c", "while true; do sleep 60; done"],
                         "background": True}
                    ],
                },
            },
        )

        # Second shell on the running sandbox (shortcut path).
        with Shell(devm, cwd=str(workspace.path)) as second:
            second.expect_prompt(timeout=60)
            second.send('echo "GOT_API=$API_LOG_LEVEL"')
            second.expect_text(r"GOT_API=debug", timeout=15)
            second.expect_prompt(timeout=15)
            second.exit(timeout=30)

        first.exit(timeout=30)

    # Anchor-alive: explicitly stop after shell exit.
    devm.stop(yes=True)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "stopped":
            return
        time.sleep(0.5)
    pytest.fail(f"sandbox {sandbox_name} never reached 'stopped'")
