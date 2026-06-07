"""27: a failing install: step makes devm shell exit non-zero with structured error.

Pin for the supervision design's install-failure UX. Before this
work, sbx surfaced install failures with rc != 0 (contract_02) but
devm just dumped the captured anchor output. With supervision,
devm pulls the failing step's per-step capture from
/tmp/.devm/install-<N>/current and formats a structured error.
"""
import subprocess

import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(120)
def test_install_step_fails_loud(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        install=["false"],
    )

    # devm shell should exit non-zero. We capture combined stderr.
    proc = subprocess.run(
        [devm.path, "shell"],
        cwd=str(workspace.path),
        capture_output=True, timeout=90,
    )
    assert proc.returncode != 0, (
        f"devm shell should exit non-zero on install failure; got rc=0\n"
        f"stdout={proc.stdout.decode()!r}\nstderr={proc.stderr.decode()!r}"
    )
    err = proc.stderr.decode()
    # bootstrap.sh is install step 1; user `false` is install step 2.
    assert "install step 2 failed" in err, (
        f"expected 'install step 2 failed' in stderr; got:\n{err}"
    )
    assert "false" in err, (
        f"expected user command 'false' in error report; got:\n{err}"
    )
