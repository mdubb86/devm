"""275: an auto-teardown-on-fail terminates the project's mutagen sessions.

Two teardown paths exist in the orchestrator: the user-driven
`devm stop --destroy` goes through RunStop and terminates sessions
(TeardownPhase), while the auto-teardown-on-fail path
(internal/orchestrator/shell.go:teardownOnFail) only *flushes* sessions
and does not terminate them. The practical consequence is that
sessions survive the destroyed VM. On the next `devm start`, the
warm-attach branch (internal/serviceapi/mutagen_sessions.go:347-376)
resumes the stale session against a fresh guest, mutagen reports
"session is not currently able to synchronize", and the cold-start
fails — the same way the leaguerboard onboarding failure looped.

What this pins:
  - After a failed install() triggers auto-teardown, no mutagen
    sessions remain for the project (`sync_list(prefix)` is empty).
  - A subsequent `devm start` with a passing install() succeeds
    without the "session is not currently able to synchronize" error.

Related:
  - test_51 covers the loud-failure contract (non-zero exit + VM gone).
  - test_114 covers templates-stage failure teardown.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.mutagen_e2e import session_prefix, sync_list
from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(600)
@pytest.mark.slow
def test_auto_teardown_clears_mutagen_sessions(devm, workspace):
    # Phase 1: force a provisioning failure.
    workspace.write_devmyaml()
    workspace.write_devm_sh(
        "#!/usr/bin/env bash\n"
        "set -eo pipefail\n"
        "install() {\n"
        "  false\n"
        "}\n"
    )

    try:
        p = subprocess.run(
            [devm.path, "start"],
            capture_output=True, cwd=str(workspace.path), timeout=180,
        )
        assert p.returncode != 0, (
            f"devm start should exit non-zero when install returns non-zero; "
            f"got rc={p.returncode}\nstderr={p.stderr.decode()}"
        )

        # VM is gone (test_51's contract) — the pre-existing shape.
        vm = TartSandbox(name=workspace.vm_name)
        current = vm.state()
        assert current == "absent", (
            f"failed install must not leave a VM behind; "
            f"VM is still in state {current!r}"
        )

        # The actual regression: mutagen sessions for this project must
        # not survive the auto-teardown. Pre-fix: sessions remain,
        # pointing at an SSH endpoint whose VM no longer exists.
        residual = sync_list(session_prefix(workspace.vm_name))
        assert residual == [], (
            f"auto-teardown-on-fail left mutagen sessions behind — the "
            f"next cold-start will warm-attach stale sessions and loop "
            f"on 'session is not currently able to synchronize'. "
            f"Residual: {[s.get('name') for s in residual]}"
        )

        # Phase 2: with a clean session table, a passing install() must
        # bring the project up normally. Pre-fix (stale sessions still
        # present): mutagen reports the inability-to-synchronize error
        # and `devm start` fails.
        workspace.write_devm_sh(
            "#!/usr/bin/env bash\n"
            "set -eo pipefail\n"
            "install() {\n"
            "  :\n"
            "}\n"
        )
        devm.approve()
        p = subprocess.run(
            [devm.path, "start"],
            capture_output=True, cwd=str(workspace.path), timeout=180,
        )
        assert p.returncode == 0, (
            f"recovery cold-start after a failed install must succeed; "
            f"got rc={p.returncode}\n"
            f"stdout: {p.stdout.decode()!r}\n"
            f"stderr: {p.stderr.decode()!r}"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
