"""36: editing a service startup + reconcile-yes recreates with the NEW startup.

KindStartupChange is in the TEARDOWN+SHELL bucket because the
startup commands are baked into the per-project systemd units at
provision time. Editing a startup command requires teardown +
recreate to take effect, not just stop + restart.

This test pins the round-trip: cold-start with v1 startup → edit to
v2 → reconcile --yes (which removes the sandbox) → new devm shell
creates a fresh sandbox running v2 startup.

The startup writes a marker so we can tell v1 from v2. Since the
sandbox is removed, v1's marker should also be gone — pinning that
reconcile-yes was a true teardown, not a stop.

What this pins:
  - cold-start runs v1 startup (writes v1 marker).
  - reconcile --yes on a startup-change REMOVES the sandbox (the
    teardown-bucket semantics).
  - next devm shell creates a fresh sandbox and runs the v2 startup
    (v2 marker present, v1 marker absent).

What it doesn't cover (tested elsewhere):
  - Non-tty + --yes prompt-flow halves -> test_09.
  - Install-change forces TEARDOWN (same bucket) -> test_14.
"""
import pytest

from helpers import Shell, stop_and_wait_stopped
from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


def _config(marker_path: str) -> dict:
    return {
        "worker": {
            "startup": [
                {"command": ["sh", "-c",
                             f"touch {marker_path} && while true; do sleep 60; done"],
                 "background": True}
            ],
        },
    }


def _ls_markers(tart_sandbox: TartSandbox) -> str:
    """Probe the VM directly for marker state — independent of any shell."""
    r = tart_sandbox.exec_shell(
        "ls /tmp/startup-marker-* 2>/dev/null || echo NONE",
        timeout=15,
    )
    return r.stdout.strip()


@pytest.mark.timeout(180)
def test_startup_change_round_trip(workspace, devm, tart_sandbox, sandbox_name):
    v1_marker = "/tmp/startup-marker-v1"
    v2_marker = "/tmp/startup-marker-v2"

    workspace.write_devmyaml(services=_config(v1_marker))

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)

        # v1 startup wrote its marker.
        sh.run_check(f"test -f {v1_marker}", expect_zero=True, timeout=15)
        sh.run_check(f"test -f {v2_marker}", expect_zero=False, timeout=15)

        # Edit -> KindStartupChange (BucketTeardownShell). Reconcile
        # --yes removes the sandbox so the new kit takes effect on
        # next create.
        workspace.patch_devmyaml(services=_config(v2_marker))
        devm.reconcile(yes=True, timeout=60, check=False)
        sh.expect_eof(timeout=30)

    # Sandbox should be GONE (teardown bucket = tart vm removed, not stopped).
    assert tart_sandbox.state() == "absent", (
        f"sandbox {sandbox_name} still exists after reconcile --yes "
        f"(startup-change should be teardown-bucket)"
    )

    # Fresh devm shell creates a new sandbox with the v2 kit.
    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)

        listing = _ls_markers(tart_sandbox)
        assert v2_marker in listing, (
            f"v2 startup did not run after recreate. Markers: {listing!r}"
        )
        assert v1_marker not in listing, (
            f"v1 marker survived teardown (should have been wiped). "
            f"Markers: {listing!r}"
        )

        sh.exit(timeout=30)

    stop_and_wait_stopped(devm, sandbox_name)
