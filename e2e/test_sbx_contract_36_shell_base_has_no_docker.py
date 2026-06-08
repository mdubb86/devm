"""contract: sbx's plain shell template does NOT ship docker.

Symmetric counterpart to test_sbx_contract_35. Devm's promise that
`base_image.docker` is a meaningful switch rests on the two templates
actually differing — `shell-docker` ships dockerd, `shell` does not.

If this test goes red, sbx started bundling docker into the plain
shell template. That's not a breakage per se, but it means devm's
docker/no-docker distinction has lost teeth and the schema field
should be reconsidered (just always-on docker, or rename the field).

What this pins:
  - Under base_image.docker:false (or unset), `command -v docker`
    returns non-zero inside the sandbox.
"""
import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(180)
def test_shell_base_has_no_docker(workspace, devm, sandbox_name):
    # Empty devm.yaml → base_image.docker defaults to false → plain shell template.
    workspace.write_devmyaml()

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=120)

        # `command -v docker` returns 0 if found, non-zero if not.
        # No stderr noise; clean yes/no signal.
        sh.run_check("command -v docker", expect_zero=False, timeout=10)

        sh.exit(timeout=30)

    stop_and_wait_stopped(devm, sandbox_name)
