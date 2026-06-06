"""24: cold start with base_image.docker: true (the DinD shell-docker base).

Locks the previously-untested branch of base_image selection. Until
this test landed (2026-06-05) every e2e test inherited workspace.py's
default base_image={'docker': False} and exercised only the plain
'shell' sbx template — meaning the 'shell-docker' (docker-in-docker)
base was 0% covered.

That gap let a regression slip: under devm's cold-start spawn shape
(nohup + Stdin=DEVNULL + Stdout/Stderr → ring buffer pipe), the
shell-docker base's "Configuring Docker" hook breaks; the inner
container exits before sbx can run its started-hook (CLAUDE.md write,
exec readiness, etc.). The same `sbx run` args invoked directly from
a terminal (tty stdio) work fine — so the bug is at the
devm-spawn-shape × DinD intersection.

Asserts: cold start with base_image.docker: true brings up a shell and
`docker --version` works inside the sandbox. Failure pins the
regression instead of letting it surface only when a real project
(like everstone) tries to use docker:true.
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
