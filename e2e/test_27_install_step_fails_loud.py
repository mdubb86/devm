"""27: a failing install: step makes devm shell exit non-zero with structured error.

Pin for the supervision design's install-failure UX. Before this
work, devm just dumped the captured anchor output. With supervision,
devm pulls the failing step's per-step capture from
/tmp/.devm/install-<N>/current and formats a structured error.
"""
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(120)
def test_install_step_fails_loud(workspace, devm):
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
    # The provisioner names the failing step in the error chain.
    assert 'provision step "run install commands"' in err, (
        f"expected 'provision step \"run install commands\"' in stderr; got:\n{err}"
    )
    assert "false" in err, (
        f"expected user command 'false' in error report; got:\n{err}"
    )
