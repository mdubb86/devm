"""01: cold-start a project from scratch -> install runs -> shell works -> stop.

`devm shell` on a project with no existing sandbox creates one, runs
the install: list from devm.yaml at create time, and opens an
interactive shell that reaches a prompt. The user exits the shell --
the sandbox stays running (anchor-alive). `devm stop --yes` then
brings it to 'stopped'.

What this pins:
  - Cold-create path runs install: at sandbox create time (verified
    via a marker file written by install).
  - Interactive shell reaches a prompt.
  - Shell exit does NOT auto-stop the sandbox (the explicit
    stop_and_wait_stopped at the end is non-redundant).
  - devm stop --yes transitions running -> stopped within ~15s.

What it doesn't cover (tested elsewhere):
  - Teardown (sandbox removal) -> test_05.
  - Cold-start with templates -> test_19.
  - Cold-start variants (docker base, curl install) -> test_24, test_25.
"""
import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(60)
def test_cold_start(workspace, devm, sandbox_name):
    workspace.write_devmyaml(install=["touch /tmp/install-marker"])

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=60)
        # install ran at create → marker present
        sh.run_check("test -e /tmp/install-marker", expect_zero=True, timeout=30)
        sh.exit(timeout=30)

    stop_and_wait_stopped(devm, sandbox_name)
