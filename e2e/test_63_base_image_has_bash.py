"""63: bash is preinstalled on the Tart base image.

Pins that bash is present on the VM before any install: step runs, so
devm-rendered shell wrappers (.devm/scripts/wrap-fg.sh, wrap-bg.sh)
can use bash-isms (pipefail, process substitution, arrays) without an
explicit apt-install bash dependency in user config.

Devm dependency: wrap-fg.sh and wrap-bg.sh use `#!/usr/bin/env bash`
and bash-specific features. If bash were absent, the wrappers would
break on first install: or startup step.

What this pins:
  - bash is on PATH inside the VM after cold-start.
  - bash executes a trivial command successfully.

What it doesn't cover (tested elsewhere):
  - bash pipe + PIPESTATUS contract -> test_64.
  - WORKSPACE_DIR in exec contexts -> test_61.
"""
from __future__ import annotations

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(120)
def test_bash_present_on_tart_base_image(tart_sandbox):
    # Belt: bash is on PATH.
    r = tart_sandbox.exec_shell("command -v bash")
    assert r.ok, (
        f"bash missing on Tart base image — 'command -v bash' failed: "
        f"stdout={r.stdout!r} stderr={r.stderr!r}"
    )
    path = r.stdout.strip()
    assert path, f"empty path from 'command -v bash': {r.stdout!r}"

    # Suspenders: bash actually executes (not just present on disk).
    r = tart_sandbox.exec("bash", "-c", 'echo "rc=$?"')
    assert r.ok and "rc=0" in r.stdout, (
        f"bash present but won't run trivial command: "
        f"stdout={r.stdout!r} stderr={r.stderr!r}"
    )
