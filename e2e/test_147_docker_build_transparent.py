"""147: docker build works with zero devm-specific Dockerfile content.

Proves the transparent-buildkit-ca-trust chain end-to-end:
  - docker build rewrites through the devm buildx builder
  - buildkitd invokes devm-runc-shim for RUN steps
  - shim bind-mounts CA bundle → RUN curl against MITM'd HTTPS succeeds
  - shim appends caenv.Vars → RUN env sees HTTPLIB2_CA_CERTS et al.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_docker_build_transparent(workspace, devm):
    workspace.write_devmyaml(
        docker=True,
        network={
            "allow": [
                "api.github.com",
                # Debian's apt mirrors — the build's `apt-get update &&
                # apt-get install -y curl` exits early with 403 without
                # these. Exercising the apt path is the point: it proves
                # multi-hop MITM'd HTTPS survives the transparent-build
                # chain, not just a single-shot curl.
                "deb.debian.org",
                "security.debian.org",
            ],
        },
    )

    start = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=600,
    )
    assert start.returncode == 0, (
        f"devm start failed:\nstderr={start.stderr.decode()!r}"
    )

    # Sanity: buildkit systemd service is up, buildx knows about devm.
    inspect = devm_exec_with_retry(
        devm.path, ["docker", "buildx", "inspect", "devm"],
        cwd=str(workspace.path), timeout=30,
    )
    assert inspect.returncode == 0, (
        f"docker buildx inspect devm failed — install script did not "
        f"register the builder, or buildkitd is down:\n"
        f"stderr={inspect.stderr.decode()!r}"
    )

    # Plain Dockerfile — zero devm-specific content.
    build_ctx = workspace.path / "transparent-build"
    build_ctx.mkdir()
    (build_ctx / "Dockerfile").write_text(
        "FROM debian:trixie-slim\n"
        "RUN apt-get update -qq && apt-get install -y -qq curl\n"
        # Assertion A: system-store CA — MITM HTTPS to allow-listed host
        # succeeds without -k because the shim bind-mounted the CA bundle.
        "RUN curl -fsS -A 'Mozilla/5.0' https://api.github.com/ > /dev/null\n"
        # Assertion B: caenv env vars — three per-library env vars from
        # caenv.Vars must be present. Line count of matches must be
        # exactly 3; any missing = env-var injection didn't reach the
        # build RUN sandbox.
        "RUN env | grep -E '^(HTTPLIB2_CA_CERTS|GRPC_DEFAULT_SSL_ROOTS_FILE_PATH|GIT_SSL_CAINFO)=' "
        "| wc -l | grep -qx 3\n"
    )
    build = devm_exec_with_retry(
        devm.path,
        ["docker", "build", "-t", "devm-transparent", "transparent-build"],
        cwd=str(workspace.path), timeout=600,
    )
    assert build.returncode == 0, (
        f"transparent docker build failed:\n"
        f"stdout={build.stdout.decode()!r}\n"
        f"stderr={build.stderr.decode()!r}\n"
        f"Failure at curl RUN → CA bind-mount didn't land in build "
        f"RUN sandbox; failure at env|grep RUN → caenv env-var "
        f"injection didn't reach it."
    )
