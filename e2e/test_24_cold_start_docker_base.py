"""24: cold start under base_image.docker: true (shell-docker DinD base) reaches an exec-ready shell with a working inner docker.

User sets `base_image: {docker: true}` and runs `devm shell`. The
DinD shell-docker base completes its "Configuring Docker" setup
under devm's nohup + DEVNULL-stdin + ring-buffer-pipe spawn shape,
the shell prompts, and `docker --version` succeeds inside the
sandbox.

What this pins:
  - base_image.docker:true selects the shell-docker sbx template
    (the only differing knob vs the default cold-start fixture).
  - Cold-start prompt arrives within the docker-base budget (180s).
  - Inner docker daemon is usable: `docker --version` returns 0.

What it doesn't cover (tested elsewhere):
  - Plain (non-docker) cold start: test_01_cold_start.
  - sbx-layer lifecycle / install / exec contracts:
    test_sbx_contract_01..07.
  - Live edits or recreate under docker:true: not yet pinned.
  - Running a real container with `docker run` inside the sandbox
    (only the daemon CLI is probed): not yet pinned.
"""
import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(240)
def test_cold_start_docker_base_brings_up_shell(workspace, devm, sandbox_name):
    # The ONLY differing knob vs every other cold-start e2e test: docker:true.
    # Keep the rest minimal so a failure clearly points at the base image,
    # not at install:, services, mounts, etc.
    workspace.write_devmyaml(
        base_image={"docker": True},
    )

    with Shell(devm, cwd=str(workspace.path)) as sh:
        # Generous timeout: the shell-docker base has a "Configuring Docker"
        # phase that takes noticeably longer than the plain shell base
        # (image pull plus inner dockerd setup).
        sh.expect_prompt(timeout=180)

        # Sandbox is exec-ready. Inner docker daemon should be available.
        sh.run_check("docker --version", expect_zero=True, timeout=15)

        sh.exit(timeout=30)

    # Anchor-alive: explicit stop after shell exit, then confirm reaped.
    stop_and_wait_stopped(devm, sandbox_name)
