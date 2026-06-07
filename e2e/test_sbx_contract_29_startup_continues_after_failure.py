"""lifecycle: sbx HALTS startup at the first failure (silent but ordered).

Probed 2026-06-07. Result: when a startup step exits non-zero, sbx
does NOT run subsequent startup steps. The sandbox still ends up
running (contract_24 — silent failure), but startup processing is
fail-fast under the hood.

Setup:
  - install: noop
  - startup step 1: sh -c 'false' (exits 1)
  - startup step 2: sh -c 'touch /tmp/step-2-ran'

Observed: /tmp/step-2-ran is ABSENT after bringup. Step 2 never ran.

Combined with contract_24, the full sbx startup semantics are:
  - First failing step: sbx stops running subsequent startup steps.
  - BUT sandbox status flips to "running" anyway and exec works.
  - sbx run output: no error message about the failure.

Devm dependency: docs/superpowers/specs/2026-06-07-startup-
supervision-design.md uses this to decide wrapper shape:
  - Sbx provides ordering+fail-fast for free; wrap-fg.sh does NOT
    need a precheck of the prior step's .ok marker.
  - Devm's startup-supervision still NEEDS its own marker scheme
    because sbx is silent (contract_24). Markers tell devm WHICH
    step failed; sbx's halt tells devm WHERE the user's intent
    stopped being honored.

Background steps are out of scope for this fail-fast question by
design: they're fire-and-forget daemons. Their wrap-bg.sh writes
.spawned and exits 0, so subsequent steps see "step ok" from sbx's
POV regardless of whether the daemon stays alive.
"""
from __future__ import annotations

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(180)
def test_startup_step_2_runs_despite_step_1_failure(sandbox_name):
    spec = minimal_kit(
        install=["true"],
        startup=[
            {
                "command": ["sh", "-c", "false"],  # exits 1
                "user": "1000",
                "description": "deliberately failing step 1",
            },
            {
                "command": ["sh", "-c", "touch /tmp/step-2-ran"],
                "user": "1000",
                "description": "step 2 marker (does sbx run me?)",
            },
        ],
    )

    with contract_sandbox(spec, sandbox_name):
        # The pin: step 2 must NOT have run (sbx halts at step 1's failure).
        # If this assertion ever fails, sbx changed: it now runs subsequent
        # startup steps despite failures, and the supervision design needs
        # to add wrapper-level fail-fast (precheck prior step's .ok marker).
        r = sbx_exec(sandbox_name, "test", "-f", "/tmp/step-2-ran")
        assert r.returncode != 0, (
            f"/tmp/step-2-ran EXISTS — sbx ran step 2 despite step 1's "
            f"failure. Startup is no longer fail-fast at the sbx level. "
            f"Update the startup-supervision design: wrap-fg.sh must "
            f"precheck the prior step's .ok marker."
        )
