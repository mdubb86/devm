"""85: `devm exec` with no argv is a usage error.

`devm exec COMMAND [ARGS...]` runs a non-interactive command inside a
*running* sandbox. This is a cobra-level Args check that fires before
we hit the running-VM gate, so it needs no VM and can stay a fast,
standalone test.

The VM-needing invariants formerly here — one-shot exec auto-cd'ing to
$WORKSPACE, and flag passthrough via DisableFlagParsing — are now
pinned in test_84, chained onto the same boot that already proves
`devm start` and the stopped-VM exec failure. Splitting them out here
would cost 2 extra VM boots for zero coverage gain.

What this pins:
  - `devm exec` with no command should fail.

What it doesn't cover (tested elsewhere):
  - `devm start` cold-start-and-return -> test_84.
  - `devm exec` one-shot auto-cd to $WORKSPACE -> test_84.
  - `devm exec` flag passthrough (DisableFlagParsing) -> test_84.
  - `devm exec` fails when VM stopped -> test_84.
  - `devm exec` fails when VM absent -> test_86.
  - Exit code propagation via shell -> test_50.
  - Env injection into wrapper -> test_26.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(60)
def test_exec_requires_command(workspace, devm):
    """`devm exec` with no positional args must fail loud, not open
    a shell or run a default. This is a cobra-level Args check that
    fires before we hit the running-VM gate — so no VM needed."""
    workspace.write_devmyaml()

    p = subprocess.run(
        [devm.path, "exec"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=30,
    )
    assert p.returncode != 0, (
        "devm exec with no command should fail"
    )
