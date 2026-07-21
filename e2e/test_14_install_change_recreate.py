"""14: editing `install` forces TEARDOWN recreate; new install runs, old state wiped.

A project's first install creates `marker-a`. After cold-start, the
user edits `install` to create `marker-b` instead and runs reconcile.
Because `install` is a TEARDOWN-bucket field, devm removes the
sandbox and relaunches it automatically in the same call (Task 7's
relaunch-on-yes flow, same pattern as test_09): the pre-existing user
shell hits EOF, and by the time reconcile returns the sandbox is
RUNNING again with the new install already applied — `marker-b`
present, `marker-a` absent (teardown wiped the old install's state).

What this pins:
  - First cold-start runs the declared install (marker-a present).
  - Editing `install` is a TEARDOWN-bucket change: reconcile rms the
    sandbox (open user shell hits EOF) and relaunches it within the
    same `devm reconcile --yes` call — sandbox ends up RUNNING, not
    absent.
  - The relaunch re-runs the NEW install (marker-b present) and wipes
    the prior container state (marker-a absent).

What it doesn't cover (tested elsewhere):
  - LIVE-bucket changes that don't recreate -> test_08, test_11,
    test_12, test_13.
  - Reconcile prompt+yes UX -> test_09.
  - Standalone teardown prompt+yes -> test_05.
  - Stop (preserves state) vs teardown (destroys state) contrast ->
    test_03, test_sbx_contract_03, test_sbx_contract_04.
"""
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

        # Edit install — TEARDOWN-bucket field. Triggers a recreate (VM rm
        # + automatic relaunch within this same reconcile call — Task 7's
        # relaunch-on-yes flow, same as test_09's "yes" path).
        #
        # devm.yaml is host-immutable (config-lock) while the VM runs;
        # unlock before editing — the recreate's relaunch re-locks it at
        # cold-start, same as the initial boot did.
        devm.unlock()
        workspace.patch_devmyaml(
            install=["touch /tmp/marker-b"],
        )
        devm.reconcile(yes=True, timeout=90, check=False)

        # User shell dies — sandbox was rm'd underneath.
        sh.expect_eof(timeout=30)

    # reconcile --yes relaunches after tearing down (same anchor-alive
    # contract as `devm start`/test_09): sandbox should be RUNNING again
    # by the time the call returns, with the NEW install already applied.
    # Give cold-start a deadline rather than asserting instantly, since
    # relaunch provisioning takes a few seconds even though the reconcile
    # call itself blocks until it's done.
    state = sandbox.wait_state("running", timeout=60)
    assert state == "running", (
        f"sandbox should be running again after reconcile --yes relaunched "
        f"it post-recreate; got {state!r}"
    )
    phase("reconcile-teardown-relaunch")

    # Verify the relaunch ran the NEW install (marker-b present) and wiped
    # the old install's state (marker-a absent), via a one-shot `devm exec`
    # rather than a fresh interactive shell — the sandbox is already up.
    r = sandbox.exec_shell("test -e /tmp/marker-b && echo yes || echo no")
    assert r.stdout.strip() == "yes", f"marker-b missing after relaunch: {r.stdout!r} {r.stderr!r}"
    r = sandbox.exec_shell("test -e /tmp/marker-a && echo yes || echo no")
    assert r.stdout.strip() == "no", f"marker-a should be gone after teardown wiped state: {r.stdout!r}"
    phase("verify")

    # Anchor-alive: explicitly stop after verifying.
    stop_and_wait_stopped(devm, sandbox_name)
    phase("stop")
