"""14: editing install forces TEARDOWN recreate — new install runs, old state gone."""
import pytest

from helpers import Shell, sbx, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(120)
def test_install_change_recreate(workspace, devm, sandbox_name, phase):
    workspace.write_devmyaml(
        install=["touch /home/agent/marker-a"],
        services={
            "worker": {
                "startup": [
                    {"command": ["sh", "-c", "while true; do sleep 60; done"],
                     "background": True}
                ],
            },
        },
    )
    phase("setup")

    # Cold start 1: install runs at create → marker-a present.
    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)
        sh.run_check("test -e /home/agent/marker-a", expect_zero=True, timeout=15)
        phase("cold-start-1")

        # Edit install — TEARDOWN-bucket field. Triggers a recreate (sbx rm).
        workspace.patch_devmyaml(
            install=["touch /home/agent/marker-b"],
            services={
                "worker": {
                    "startup": [
                        {"command": ["sh", "-c", "while true; do sleep 60; done"],
                         "background": True}
                    ],
                },
            },
        )
        devm.reconcile(yes=True, timeout=90, check=False)

        # User shell dies — sandbox was rm'd underneath.
        sh.expect_eof(timeout=30)

    assert not sbx.sandbox_exists(sandbox_name), "sandbox still exists after teardown recreate"
    phase("reconcile-teardown")

    # Cold start 2: fresh create re-runs the NEW install. In ONE fresh shell:
    # marker-b present (new install ran) AND marker-a absent (teardown wiped state).
    with Shell(devm, cwd=str(workspace.path)) as fresh:
        fresh.expect_prompt(timeout=90)
        fresh.run_check("test -e /home/agent/marker-b", expect_zero=True, timeout=15)
        fresh.run_check("test -e /home/agent/marker-a", expect_zero=False, timeout=15)
        fresh.exit(timeout=30)

    # Anchor-alive: explicitly stop after shell exit.
    stop_and_wait_stopped(devm, sandbox_name)
    phase("cold-start-2+verify")
