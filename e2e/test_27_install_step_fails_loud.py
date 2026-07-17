"""27: a failing install: step makes devm shell exit non-zero with structured error.

Pin for the boot-integrity-gate provisioning UX. The guest runs one
composed bash script (internal/render.RenderProvisionScript) instead
of the old per-step provisioner; a failing install: command fails the
whole "install" STAGE, and internal/provision.StepFailure/RunShell's
teardown-on-fail report `provision stage "install": provisioning
script exited <N>` on stderr — no per-command echo anymore.
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
    # The composed script names the failing STAGE (not the individual
    # command — the old per-step provisioner's per-command echo is gone).
    assert 'provision stage "install"' in err, (
        f"expected 'provision stage \"install\"' in stderr; got:\n{err}"
    )
    # ...and surfaces the exit detail the composed script DOES report on a
    # stage failure: the script's own exit code (StepFailure's wrapped err).
    assert "provisioning script exited" in err, (
        f"expected 'provisioning script exited' detail in stderr; got:\n{err}"
    )
