"""startup: step N+1 sees step N's filesystem effects.

Pins that startup: runs sequentially with completion ordering — step
N+1 starts only after step N has FINISHED, so it can rely on step N's
filesystem side effects.

Setup:
  - startup step 1: sh -c 'touch /tmp/s1-ran'
  - startup step 2: sh -c 'test -f /tmp/s1-ran && touch /tmp/s2-saw-s1'

After bringup, /tmp/s2-saw-s1 must exist — proves step 2 ran AND
saw step 1's marker. If sbx parallelized startup or didn't wait for
step 1 to finish before launching step 2, /tmp/s2-saw-s1 might be
absent (step 2 raced step 1 and failed the test).

Per contract_24, sbx is silent on startup failure, so a failing step 2
wouldn't manifest as a sbx-level error. The probe relies on observing
the marker, not on sbx's reaction.

Devm dependency: docs/superpowers/specs/2026-06-07-startup-
supervision-design.md treats startup: as ordered with completion
semantics. Combined with contract_29 (fail-fast halt) and contract_24
(silent failure), contract_31 locks the property the supervision
wrapper depends on: ordered execution with side-effect visibility.
"""
from __future__ import annotations

import pytest

from helpers.contract import contract_sandbox, minimal_kit, sbx_exec

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(120)
def test_startup_step_2_sees_step_1_effects(sandbox_name):
    spec = minimal_kit(
        install=["true"],
        startup=[
            {
                "command": ["sh", "-c", "touch /tmp/s1-ran"],
                "user": "1000",
                "description": "step 1: write a marker",
            },
            {
                "command": ["sh", "-c", "test -f /tmp/s1-ran && touch /tmp/s2-saw-s1"],
                "user": "1000",
                "description": "step 2: read marker AND write our own",
            },
        ],
    )

    with contract_sandbox(spec, sandbox_name):
        # Step 1's marker must be present (it ran).
        r = sbx_exec(sandbox_name, "test", "-f", "/tmp/s1-ran")
        assert r.returncode == 0, (
            f"/tmp/s1-ran missing — startup step 1 didn't run."
        )

        # The contract pin: step 2 must have seen step 1's effect.
        # If absent, sbx parallelized startup or didn't wait for step 1
        # to complete before launching step 2.
        r = sbx_exec(sandbox_name, "test", "-f", "/tmp/s2-saw-s1")
        assert r.returncode == 0, (
            f"/tmp/s2-saw-s1 absent — startup step 2 either didn't run "
            f"or ran without seeing step 1's marker. Startup completion "
            f"ordering broken. The startup-supervision design's "
            f"'step N+1 can depend on step N' property no longer holds."
        )
