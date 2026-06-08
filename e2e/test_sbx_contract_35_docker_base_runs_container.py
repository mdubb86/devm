"""contract: sbx's shell-docker template ships a working dockerd that pulls and runs containers.

Devm depends on this: setting `base_image.docker: true` switches the
sbx kit to the `docker/sandbox-templates:shell-docker` template, and
devm's promise to the user is that the resulting sandbox can run
containers. If this test goes red, sbx's docker template broke.

Complements test_24 (which only probes `docker --version` — CLI
presence, not daemon functionality). test_24's own docstring flags
this gap: "Running a real container with `docker run` inside the
sandbox (only the daemon CLI is probed): not yet pinned."

What this pins:
  - Inner dockerd accepts `docker run`, pulls an image from Docker
    Hub through sbx's network policy, runs the container to rc=0.
  - The `*.docker.io` / `*.docker.com` allowed_domains entries are
    sufficient for inner-dockerd to reach the registry.

What it doesn't cover:
  - Live reconcile under docker:true (env changes, etc).
  - Recreate cycle under docker:true.

If this fails with a "Blocked by network policy" body in the output,
the allowed_domains list needs expansion for the network the inner
dockerd actually hits.
"""
import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(300)
def test_docker_run_hello_world_inside_sandbox(workspace, devm, sandbox_name):
    # Allow Docker Hub explicitly so the test doesn't depend on whatever
    # sbx's default network policy happens to be when allowed_domains
    # is empty.
    workspace.write_devmyaml(
        base_image={"docker": True},
        network={
            "allowed_domains": [
                "*.docker.io",
                "*.docker.com",
            ],
        },
    )

    with Shell(devm, cwd=str(workspace.path)) as sh:
        # docker-base cold-start budget (same as test_24): the
        # "Configuring Docker" phase plus image pull can take a while.
        sh.expect_prompt(timeout=180)

        # hello-world is the canonical "does the daemon actually work"
        # check: ~13 KB image, prints a fixed banner, exits 0. Generous
        # timeout for the first-time pull from Docker Hub.
        sh.run_check("docker run --rm hello-world", expect_zero=True, timeout=90)

        sh.exit(timeout=30)

    stop_and_wait_stopped(devm, sandbox_name)
