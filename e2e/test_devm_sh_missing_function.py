"""Load-time validation: devm.yaml references a function that doesn't exist in devm.sh.

Pins the validation from Task 6: config.Load checks that every function
reference in devm.yaml resolves against Config.Functions (populated by
scriptfile.Parse reading devm.sh + devm.me.sh).
"""
from __future__ import annotations

import subprocess
import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(120)
def test_devm_yaml_references_undefined_function(workspace, devm):
    """devm.yaml declares repos.main.commands: [nope], but devm.sh doesn't
    define nope(). devm start must fail at load with an error naming both
    the field (repos.main.commands) and the missing function (nope).
    """
    workspace.write_devmyaml(
        repos={"main": {"url": workspace.bare_repo_url(), "primary": True, "commands": ["nope"]}}
    )
    workspace.write_devm_sh("something-else() { true; }\n")

    result = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=60,
    )
    assert result.returncode != 0, "devm start unexpectedly succeeded"

    stderr = result.stderr.decode()
    assert "nope" in stderr, (
        f"expected error message to name the missing function 'nope'; got:\n"
        f"stderr={stderr!r}"
    )
    assert "not defined in devm.sh" in stderr, (
        f"expected error message to indicate function not defined; got:\n"
        f"stderr={stderr!r}"
    )
    assert "commands" in stderr, (
        f"expected error message to name the field 'commands'; got:\n"
        f"stderr={stderr!r}"
    )
