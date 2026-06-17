"""33: docker run inside the DinD shell-docker base actually runs a container.

test_24 pins that the inner `docker --version` succeeds, proving the
daemon CLI is reachable. This test goes a step further: it pulls and
runs a real container inside the sandbox, proving the DinD daemon
can actually create + execute containers (volume mounts, networking,
process isolation — all the bits that distinguish "the binary
exists" from "DinD works").

Uses `hello-world` (Docker's smallest official image, ~17KB) for a
fast, hermetic-ish probe. Requires network access to pull from Docker
Hub on first run; `registry-1.docker.io` and `auth.docker.io` must be
in the kit's allowed_domains. If a future sbx tightens the install:
network policy, this test surfaces it via the docker pull failing.

What this pins:
  - `docker run --rm hello-world` inside the sandbox returns 0.
  - The container's stdout contains "Hello from Docker!" (the
    official image's banner line) — proves stdout actually flows
    out of the container, through the docker daemon, back to the
    sandbox shell.

What it doesn't cover (tested elsewhere):
  - Base image selection / daemon-CLI reachability: test_24.
  - Live edits under docker:true: not yet pinned.
"""
import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(240)
def test_docker_base_runs_container(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        base_image={"docker": True},
        network={"allowed_domains": [
            "registry-1.docker.io",
            "auth.docker.io",
            "production.cloudflare.docker.com",
        ]},
    )

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=180)

        # Pull + run + clean up in one go. --rm removes the container
        # so the test leaves no in-sandbox docker state behind.
        sh.send("docker run --rm hello-world")
        sh.expect_text(r"Hello from Docker!", timeout=120)
        sh.expect_prompt(timeout=10)

        # Belt: a follow-up `docker ps -a` should show no containers
        # (the --rm worked).
        sh.run_check(
            "test $(docker ps -a --format '{{.Names}}' | wc -l) -eq 0",
            expect_zero=True, timeout=10,
        )

        sh.exit(timeout=30)

    stop_and_wait_stopped(devm, sandbox_name)
