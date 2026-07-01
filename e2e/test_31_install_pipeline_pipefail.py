"""31: an install: step with a failing pipeline fails loud (pipefail in user shell).

Before this work, user install commands ran under `sh -c "<cmd>"`. On
the docker-templates:shell-docker base image (Ubuntu), `/bin/sh` is dash
and dash doesn't support `set -o pipefail`. That meant a recipe like
`curl https://x | bash` would silently report success if curl 404'd
(curl fails, bash gets no input, exits 0, pipeline exits 0, wrapper
records 0).

The fix in internal/provision/provision.go runs user install commands under
`bash -o pipefail -c "<cmd>"` so a non-zero exit anywhere in the
pipeline propagates as the pipeline's rc. This test pins that.
"""
import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(120)
def test_install_pipeline_failure_fails_loud(workspace, devm):
    # `false | cat` exits 0 without pipefail (cat's rc), non-zero WITH
    # pipefail (false's rc propagates). This is the canonical test for
    # pipefail being active.
    workspace.write_devmyaml(
        install=["false | cat"],
    )

    proc = subprocess.run(
        [devm.path, "shell"],
        cwd=str(workspace.path),
        capture_output=True, timeout=90,
    )
    assert proc.returncode != 0, (
        f"devm shell should exit non-zero on `false | cat` install step "
        f"(pipefail must be active in the user shell); got rc=0\n"
        f"stdout={proc.stdout.decode()!r}\nstderr={proc.stderr.decode()!r}"
    )
    err = proc.stderr.decode()
    # The provisioner names the failing step in the error chain.
    assert 'provision step "run install commands"' in err, (
        f"expected 'provision step \"run install commands\"' in stderr; got:\n{err}"
    )
