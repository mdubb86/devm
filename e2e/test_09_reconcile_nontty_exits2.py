"""09: non-TTY reconcile of a recreate-required change exits 2, no recreate."""
import json
import subprocess
import time

import pytest

from helpers import Shell, sbx

pytestmark = pytest.mark.devm


@pytest.mark.timeout(60)
def test_reconcile_nontty_exits2(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        install=["touch /tmp/install-marker"],
        services={
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

        # install is in the TEARDOWN bucket — changing it forces a recreate.
        workspace.patch_devmyaml(install=["touch /tmp/different-marker"])

        # Run reconcile --json with stdin from /dev/null (non-TTY).
        # Expect exit 2 and JSON with next_action=needs_approval. _run()
        # inherits the test process's stdin, which may be a TTY; we must
        # explicitly detach to exercise the non-TTY guard.
        p = subprocess.run(
            [devm.path, "reconcile", "--json"],
            cwd=str(workspace.path),
            stdin=subprocess.DEVNULL,
            capture_output=True, timeout=60, check=False,
        )
        assert p.returncode == 2, (
            f"expected exit 2 (non-TTY recreate); got {p.returncode}\n"
            f"stdout: {p.stdout.decode()!r}\nstderr: {p.stderr.decode()!r}"
        )
        body = json.loads(p.stdout.decode())
        assert body.get("next_action") == "needs_approval", (
            f"expected next_action=needs_approval; got {body}"
        )

        # The user shell must still be alive — reconcile didn't recreate.
        sh.run_check("echo still-here", expect_zero=True, timeout=15)

        sh.exit(timeout=30)

    # Anchor-alive: explicitly stop after shell exit.
    devm.stop(yes=True)
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "stopped":
            return
        time.sleep(0.5)
    pytest.fail(f"sandbox {sandbox_name} never reached 'stopped'")
