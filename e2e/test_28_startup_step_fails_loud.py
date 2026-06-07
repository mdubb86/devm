"""28: a failing startup: step makes devm shell exit non-zero with captured stderr.

Pin for the supervision design's startup-failure UX. Before this
work, sbx was silent on startup failure (contract_24); devm
silently let the user into a half-broken shell. With supervision,
devm catches the missing startup-all-ok sentinel and reports.
"""
import subprocess

import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(120)
def test_startup_step_fails_loud(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        services={
            "api": {
                "port": 8080,
                "startup": [
                    {"command": ["sh", "-c", "echo 'something broke' 1>&2; exit 1"]}
                ],
            },
        },
    )

    proc = subprocess.run(
        [devm.path, "shell"],
        cwd=str(workspace.path),
        capture_output=True, timeout=90,
    )
    assert proc.returncode != 0, (
        f"devm shell should exit non-zero on startup failure; got rc=0\n"
        f"stderr={proc.stderr.decode()!r}"
    )
    err = proc.stderr.decode()
    assert "startup step" in err and "failed" in err, (
        f"expected 'startup step ... failed' in stderr; got:\n{err}"
    )
    assert "something broke" in err, (
        f"expected captured stderr 'something broke' in error report; got:\n{err}"
    )
