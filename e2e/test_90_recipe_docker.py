"""90: docker recipe — version check, container egress, host→container.

Exercises the docker recipe end-to-end. All three tests share the same
devm.yaml (defined below) so the cost of installing Docker + pulling
images is amortized when tests run against the same VM under one
`devm start`.

Because Docker itself pulls images from Docker Hub, the recipe declares
`network.allow` with the three Docker Hub hosts. Tests also verify a
*non*-allow-listed host is blocked at iron-proxy — that's the pin that
proves container egress actually funnels through iron-proxy rather
than escaping via Docker's forward-hook bypass.

What this pins:
  - `docker --version` reports engine version (install succeeded, socket
    usable without sudo).
  - Container reaching an allow-listed host over HTTPS succeeds.
  - Container reaching a NON-allow-listed host gets rejected by
    iron-proxy (403). Not a network error — an application-layer
    reject that only happens if the packet reached iron-proxy.
  - Host process on the guest can connect to a container's published
    port on 127.0.0.1 (user_output rule opens 172.16.0.0/12 for the
    filter chain post-Docker-DNAT).

What it doesn't cover:
  - Compose networks specifically (though the 172.16/12 accept covers
    the whole 172.17-172.31 range user-defined nets use).
  - Published ports on 80/443 (documented gap — devm_nat hijacks
    those to iron-proxy; would need a user_nat chain).
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.recipe_docker


# The one devm.yaml every test in this file uses. Extracted so the tests
# themselves stay focused on assertions. Matches recipes/service/docker.md.
DOCKER_YAML = {
    "install": [
        # 1. Install Docker Engine.
        "curl -fsSL https://get.docker.com | sh && sudo usermod -aG docker devm",
        # 2. Socket permission drop-in — makes /run/docker.sock world-writable
        #    inside the VM, so `devm exec docker ...` works without needing a
        #    fresh login for the group change to take effect.
        (
            "sudo install -d /etc/systemd/system/docker.service.d && "
            "printf '%s\\n' '[Service]' 'ExecStartPost=/bin/chmod 666 /run/docker.sock' | "
            "sudo tee /etc/systemd/system/docker.service.d/override.conf >/dev/null && "
            "sudo systemctl daemon-reload && sudo systemctl restart docker"
        ),
        # 3. user_output rule for host→container reachability on non-80/443
        #    (the 80/443 case is a documented gap — hijacked by devm_nat's
        #    OUTPUT DNAT).
        "sudo nft add rule inet devm_filter user_output ip daddr 172.16.0.0/12 accept",
    ],
    "network": {
        "allow": [
            # Docker Hub — required for `docker pull` on the default registry.
            "registry-1.docker.io",
            "auth.docker.io",
            "production.cloudfront.docker.com",
            # For the container-egress test: httpbin.org is a stable
            # HTTP-test service iron-proxy can allow through. NOT
            # docker-recipe-required; test-specific.
            "httpbin.org",
        ],
    },
}


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_docker_version(workspace, devm):
    """Docker Engine installed, `docker --version` prints, socket
    usable without sudo (proves the ExecStartPost drop-in worked)."""
    workspace.write_devmyaml(**DOCKER_YAML)

    start = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=540,  # Docker install + `apt-get install` is slow.
    )
    assert start.returncode == 0, (
        f"devm start failed (docker install path):\n"
        f"stderr={start.stderr.decode()!r}"
    )

    # No sudo — the socket permission drop-in should have chmod'd
    # /run/docker.sock 666 by now.
    p = devm_exec_with_retry(
        devm.path,
        ["docker", "--version"],
        cwd=str(workspace.path),
        timeout=30,
    )
    assert p.returncode == 0, (
        f"docker --version failed: rc={p.returncode}\n"
        f"stdout={p.stdout.decode()!r}\nstderr={p.stderr.decode()!r}"
    )
    out = p.stdout.decode()
    assert "Docker version" in out, (
        f"expected 'Docker version' in output; got {out!r}"
    )

    # Sanity: the socket also works for a command that actually contacts
    # dockerd. `docker info` fails if the socket isn't reachable.
    info = devm_exec_with_retry(
        devm.path,
        ["docker", "info"],
        cwd=str(workspace.path),
        timeout=30,
    )
    assert info.returncode == 0, (
        f"docker info failed — likely socket permission issue:\n"
        f"stderr={info.stderr.decode()!r}"
    )


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_container_egress_through_iron_proxy(workspace, devm):
    """Container-issued HTTP hits iron-proxy: allow-listed target
    succeeds, non-allow-listed target gets rejected."""
    workspace.write_devmyaml(**DOCKER_YAML)

    start = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=540,
    )
    assert start.returncode == 0, (
        f"devm start failed:\n{start.stderr.decode()!r}"
    )

    # Allow-listed host from inside a container — should succeed.
    # httpbin.org's /status/200 always returns 200 OK.
    allowed = devm_exec_with_retry(
        devm.path,
        ["docker", "run", "--rm", "curlimages/curl:latest",
         "-sf", "-o", "/dev/null", "-w", "%{http_code}",
         "--max-time", "15", "https://httpbin.org/status/200"],
        cwd=str(workspace.path),
        timeout=120,
    )
    assert allowed.returncode == 0, (
        f"allow-listed container HTTPS failed: rc={allowed.returncode}\n"
        f"stdout={allowed.stdout.decode()!r}\n"
        f"stderr={allowed.stderr.decode()!r}"
    )
    got_code = allowed.stdout.decode().strip().splitlines()[-1]
    assert got_code == "200", (
        f"expected 200 from httpbin.org via container; got {got_code!r}"
    )

    # Non-allow-listed host from inside a container — iron-proxy
    # should reject with 403. curl -sf exits non-zero on any HTTP >=
    # 400, so we don't need to check the exact code; just that the
    # attempt fails at the HTTP layer (packet reached iron-proxy) as
    # opposed to failing at TCP/DNS (packet escaped the allow-list).
    blocked = devm_exec_with_retry(
        devm.path,
        ["docker", "run", "--rm", "curlimages/curl:latest",
         "-s", "-o", "/dev/null", "-w", "%{http_code}",
         "--max-time", "15", "https://google.com/"],
        cwd=str(workspace.path),
        timeout=120,
    )
    # -s (silent) + no -f means curl doesn't exit non-zero on HTTP
    # errors, so returncode is 0 and stdout has the code.
    assert blocked.returncode == 0, (
        f"curl itself failed (network error not HTTP reject):\n"
        f"stderr={blocked.stderr.decode()!r}"
    )
    got_code = blocked.stdout.decode().strip().splitlines()[-1]
    assert got_code == "403", (
        f"non-allow-listed google.com from container should return "
        f"403 (iron-proxy reject); got {got_code!r}. If got 000, the "
        f"connection failed at network layer — packet escaped without "
        f"hitting iron-proxy at all, which is the exact leak this "
        f"test is designed to catch."
    )


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_host_can_reach_published_container_port(workspace, devm):
    """Container publishes port 12345, guest process connects to
    127.0.0.1:12345 and reaches the container. Pins that the
    user_output rule for 172.16.0.0/12 lets host→container traffic
    through the filter chain post-Docker-DNAT."""
    workspace.write_devmyaml(**DOCKER_YAML)

    start = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=540,
    )
    assert start.returncode == 0, (
        f"devm start failed:\n{start.stderr.decode()!r}"
    )

    # Run a tiny nginx container publishing 80 (in the container) to
    # 127.0.0.1:12345 (on the guest). Non-80/443 port on the host
    # side deliberately — the 80/443 hijack by devm_nat's OUTPUT
    # DNAT is a known gap documented in the recipe.
    #
    # -d detached, --rm cleanup on stop.
    run = devm_exec_with_retry(
        devm.path,
        ["docker", "run", "-d", "--rm", "--name", "e2e-web",
         "-p", "127.0.0.1:12345:80", "nginx:alpine"],
        cwd=str(workspace.path),
        timeout=180,
    )
    assert run.returncode == 0, (
        f"docker run nginx failed: rc={run.returncode}\n"
        f"stderr={run.stderr.decode()!r}"
    )

    try:
        # Give nginx a moment to bind. `docker run -d` returns as soon
        # as the container starts, before entrypoint's listener is
        # ready.
        import time
        deadline = time.time() + 15
        got_code = ""
        while time.time() < deadline:
            hit = devm_exec_with_retry(
                devm.path,
                ["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
                 "--max-time", "5", "http://127.0.0.1:12345/"],
                cwd=str(workspace.path),
                timeout=30,
            )
            got_code = hit.stdout.decode().strip().splitlines()[-1] if hit.stdout else ""
            if got_code == "200":
                break
            time.sleep(1)
        assert got_code == "200", (
            f"guest→127.0.0.1:12345→container:80 should return 200 "
            f"(nginx default welcome page); got {got_code!r}. If got "
            f"000 or timeout, the filter chain dropped the packet — "
            f"user_output 172.16.0.0/12 rule not applied."
        )
    finally:
        # Cleanup the nginx container.
        subprocess.run(
            [devm.path, "exec", "docker", "rm", "-f", "e2e-web"],
            cwd=str(workspace.path),
            capture_output=True,
            timeout=30,
        )
