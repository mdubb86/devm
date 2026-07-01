"""14: editing `install` forces TEARDOWN recreate; new install runs, old state wiped.

A project's first install creates `marker-a`. After cold-start, the
user edits `install` to create `marker-b` instead and runs reconcile.
Because `install` is a TEARDOWN-bucket field, devm removes the
sandbox (anchor + state gone): the pre-existing user shell hits EOF,
and the VM is absent. A second cold-start then runs the NEW
install — a fresh shell sees `marker-b` present AND `marker-a` absent,
proving the recreate ran the edited install and that the prior
container state was discarded (not preserved across teardown).

What this pins:
  - First cold-start runs the declared install (marker-a present).
  - Editing `install` is a TEARDOWN-bucket change: reconcile rms the
    sandbox; the open user shell hits EOF, TartSandbox.state() is absent.
  - The next cold-start re-runs the NEW install (marker-b present).
  - Teardown wipes prior container state — old marker-a is gone in the
    fresh sandbox.

What it doesn't cover (tested elsewhere):
  - LIVE-bucket changes that don't recreate -> test_08, test_11,
    test_12, test_13.
  - Reconcile prompt+yes UX -> test_09.
  - Standalone teardown prompt+yes -> test_05.
  - Stop (preserves state) vs teardown (destroys state) contrast ->
    test_03, test_sbx_contract_03, test_sbx_contract_04.
"""
import time

import pytest

from helpers import Shell, stop_and_wait_stopped
from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(120)
def test_install_change_recreate(workspace, devm, sandbox_name, phase):
    workspace.write_devmyaml(
        install=["touch /tmp/marker-a"],
    )
    phase("setup")
    sandbox = TartSandbox(name=sandbox_name)

    # Cold start 1: install runs at create → marker-a present.
    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)
        sh.run_check("test -e /tmp/marker-a", expect_zero=True, timeout=15)
        phase("cold-start-1")

        # Edit install — TEARDOWN-bucket field. Triggers a recreate (VM rm).
        workspace.patch_devmyaml(
            install=["touch /tmp/marker-b"],
        )
        devm.reconcile(yes=True, timeout=90, check=False)

        # User shell dies — sandbox was rm'd underneath.
        sh.expect_eof(timeout=30)

    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sandbox.state() == "absent":
            break
        time.sleep(0.5)
    assert sandbox.state() == "absent", "sandbox still exists after teardown recreate"
    phase("reconcile-teardown")

    # Cold start 2: fresh create re-runs the NEW install. In ONE fresh shell:
    # marker-b present (new install ran) AND marker-a absent (teardown wiped state).
    with Shell(devm, cwd=str(workspace.path)) as fresh:
        fresh.expect_prompt(timeout=90)
        fresh.run_check("test -e /tmp/marker-b", expect_zero=True, timeout=15)
        fresh.run_check("test -e /tmp/marker-a", expect_zero=False, timeout=15)
        fresh.exit(timeout=30)

    # Anchor-alive: explicitly stop after shell exit.
    stop_and_wait_stopped(devm, sandbox_name)
    phase("cold-start-2+verify")
