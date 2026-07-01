"""35: editing `path:` and reconciling propagates to the NEXT shell (LIVE).

Per the schema doc, path: is in the LIVE bucket and follows the same
.devm/.env fan-out as cfg.Env: a `devm reconcile` rewrites the env
file but already-attached shells keep their old PATH. New shells
(re-sourced via with-devm-env) pick up the change.

This test pins both halves of that contract:
  1. A LIVE reconcile of a path: edit does NOT affect the
     already-attached shell's $PATH.
  2. The NEXT devm shell (warm-attached to the same running sandbox)
     sees the new entry at the head of $PATH.

What this pins:
  - path: change is LIVE-bucket (no sandbox restart).
  - First shell's $PATH stays unchanged after reconcile.
  - Second shell sees the new $WORKSPACE/bin head.

What it doesn't cover (tested elsewhere):
  - Cold-start path: -> test_34.
  - Validation rejection -> schema unit tests.
"""
import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.xfail(
    strict=False,
    reason=(
        "devm bug D: WriteSnapshot uses hardcoded /home/agent/.devm/ which does not "
        "exist in Tart VMs (admin user). reconcile --yes exits 1 on the snapshot "
        "write, so the path: live-change cannot be applied or verified. "
        "Remove xfail when bug D lands."
    ),
)
@pytest.mark.timeout(120)
def test_path_field_live_change(workspace, devm, sandbox_name):
    # Cold-start with NO path: entry.
    workspace.write_devmyaml()

    expected_head = f"{workspace.path}/bin"

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=60)

        # First shell: $PATH must NOT yet contain $WORKSPACE/bin.
        sh.run_check(
            f"echo $PATH | grep -q '{expected_head}'",
            expect_zero=False, timeout=10,
        )

        # Add the path: entry.
        workspace.patch_devmyaml(path=["$WORKSPACE/bin"])
        devm.reconcile(yes=True, timeout=60)

        # First shell still sees the OLD $PATH (running-shell behavior).
        sh.run_check(
            f"echo $PATH | grep -q '{expected_head}'",
            expect_zero=False, timeout=10,
        )

        sh.exit(timeout=30)

    # Second shell: warm-attach should re-source .devm/.env via
    # with-devm-env and pick up the new head.
    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=60)
        sh.run_check(
            f"awk -v p=\"$PATH\" -v want='{expected_head}:' "
            "'BEGIN{exit (index(p, want) == 1) ? 0 : 1}'",
            expect_zero=True, timeout=10,
        )
        sh.exit(timeout=30)

    stop_and_wait_stopped(devm, sandbox_name)
