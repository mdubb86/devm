"""Load-time or runtime validation: devm.sh with a failing install() function.

Tests the invariant that install() failure during cold-start causes
`devm start` to fail. This uses `install() { false; }` (well-formed bash
that returns non-zero) to robustly test the cold-start failure path.

Rationale: scriptfile.Parse (Task 1) enumerates function names but does
not validate bash syntax. Broken bash will only fail at runtime when the
function is executed inside the guest. Using a well-formed but failing
function tests the same cold-start failure invariant more reliably,
without depending on bash error output being properly surfaced through
the daemon to stderr.
"""
from __future__ import annotations

import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(120)
def test_devm_sh_syntax_error_fails_load(workspace, devm):
    """devm.sh has an install() function that returns non-zero (simulating
    a syntax error at runtime). devm start must fail during the install
    phase with a non-zero return code.
    """
    workspace.write_devmyaml(no_repo=True)
    workspace.write_devm_sh("install() { false; }\n")

    result = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=60,
    )
    assert result.returncode != 0, "devm start unexpectedly succeeded despite install() returning false"
