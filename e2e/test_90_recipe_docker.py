"""90: docker recipe — everything asserted in one VM lifetime.

Consolidated into a single test because `devm start` on the docker
recipe takes ~5 min (get.docker.com install + `apt-get install`), and
splitting the assertions across multiple tests would triple that cost
without adding coverage.

What this pins:
  - Docker Engine installs and `docker info` works without sudo
    (proves the ExecStartPost /run/docker.sock chmod drop-in worked).
  - Container reaching an allow-listed host over HTTPS succeeds
    (packet reached iron-proxy, iron-proxy allowed).
  - Container reaching a NON-allow-listed host gets a 403 from
    iron-proxy — not a 000 / timeout. This is the key pin: 403 means
    the packet hit iron-proxy's application layer; 000 would mean it
    escaped through Docker's forward hook without touching iron-proxy
    at all.
  - Host process on the guest can connect to a container's published
    port on 127.0.0.1:<non-80/443>. Pins the user_output rule for
    172.16.0.0/12 lets host→container traffic through the filter
    chain post-Docker-DNAT.

What it doesn't cover:
  - Compose networks specifically (though 172.16/12 covers the whole
    172.17-172.31 range user-defined nets use).
  - Published ports on 80/443 (documented gap in the recipe —
    devm_nat OUTPUT DNAT hijacks those to iron-proxy; needs a
    user_nat chain, deferred).
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.recipe


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_docker_recipe_end_to_end(workspace, devm):
    # Recipe from recipes/service/docker.md, extended with httpbin.org
    # (allow-listed test target) in network.allow.
    workspace.write_devmyaml(
        install=[
            # Docker Engine.
            "curl -fsSL https://get.docker.com | sh && sudo usermod -aG docker devm",
            # Socket permission drop-in so /run/docker.sock is usable
            # without a fresh login for the docker group change.
            (
                "sudo install -d /etc/systemd/system/docker.service.d && "
                "printf '%s\\n' '[Service]' 'ExecStartPost=/bin/chmod 666 /run/docker.sock' | "
                "sudo tee /etc/systemd/system/docker.service.d/override.conf >/dev/null && "
                "sudo systemctl daemon-reload && sudo systemctl restart docker"
            ),
            # Host → container reachability (non-80/443).
            "sudo nft add rule inet devm_filter user_output ip daddr 172.16.0.0/12 accept",
        ],
        network={
            "allow": [
                # Docker Hub (recipe-declared).
                "registry-1.docker.io",
                "auth.docker.io",
                "production.cloudfront.docker.com",
                # Test target — httpbin is a stable HTTP-test service.
                "httpbin.org",
            ],
        },
    )

    start = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=600,  # Docker install is slow.
    )
    assert start.returncode == 0, (
        f"devm start failed:\nstderr={start.stderr.decode()!r}"
    )

    # ---- Assertion 1: docker installed, socket usable without sudo. ----
    version = devm_exec_with_retry(
        devm.path, ["docker", "--version"],
        cwd=str(workspace.path), timeout=30,
    )
    assert version.returncode == 0, (
        f"docker --version failed: rc={version.returncode}\n"
        f"stderr={version.stderr.decode()!r}"
    )
    assert "Docker version" in version.stdout.decode(), (
        f"expected 'Docker version' in output; got "
        f"{version.stdout.decode()!r}"
    )
    info = devm_exec_with_retry(
        devm.path, ["docker", "info"],
        cwd=str(workspace.path), timeout=30,
    )
    assert info.returncode == 0, (
        f"docker info failed — socket permission likely broken:\n"
        f"stderr={info.stderr.decode()!r}"
    )

    # ---- Diagnostic: what does a container see for DNS? ----
    # If container HTTPS fails downstream, this points at whether DNS
    # or the DNAT is the broken piece.
    dns_diag = devm_exec_with_retry(
        devm.path,
        ["docker", "run", "--rm", "alpine:latest",
         "sh", "-c",
         "echo '--- /etc/resolv.conf ---'; cat /etc/resolv.conf; "
         "echo '--- nslookup httpbin.org ---'; nslookup httpbin.org 2>&1 || true; "
         "echo '--- nslookup google.com ---'; nslookup google.com 2>&1 || true"],
        cwd=str(workspace.path), timeout=120,
    )
    print(
        f"container DNS diagnostic:\n"
        f"stdout={dns_diag.stdout.decode()}\n"
        f"stderr={dns_diag.stderr.decode()}",
        flush=True,
    )

    # ---- Assertion 2: container HTTPS to an allow-listed host works. ----
    allowed = devm_exec_with_retry(
        devm.path,
        ["docker", "run", "--rm", "curlimages/curl:latest",
         "-sf", "-o", "/dev/null", "-w", "%{http_code}",
         "--max-time", "15", "https://httpbin.org/status/200"],
        cwd=str(workspace.path), timeout=180,
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

    # ---- Assertion 3: container HTTPS to a BLOCKED host returns 403,
    #      not a timeout. The 403 comes from iron-proxy; a timeout
    #      would mean the packet escaped iron-proxy entirely (the
    #      exact leak this test is designed to catch).
    blocked = devm_exec_with_retry(
        devm.path,
        ["docker", "run", "--rm", "curlimages/curl:latest",
         "-s", "-o", "/dev/null", "-w", "%{http_code}",
         "--max-time", "15", "https://google.com/"],
        cwd=str(workspace.path), timeout=180,
    )
    assert blocked.returncode == 0, (
        f"curl itself failed at network layer, not HTTP layer — this "
        f"is inconclusive. stderr={blocked.stderr.decode()!r}"
    )
    got_code = blocked.stdout.decode().strip().splitlines()[-1]
    assert got_code == "403", (
        f"non-allow-listed google.com from container should return "
        f"403 (iron-proxy reject); got {got_code!r}. Getting 000 "
        f"here would mean the container's egress bypassed iron-proxy "
        f"entirely — the FORWARD-hook enforcement isn't working."
    )

    # ---- Assertion 4: host → container reachability on published port. ----
    #
    # Non-80/443 port on the host side — the 80/443 hijack by devm_nat's
    # OUTPUT DNAT is a known gap.
    run = devm_exec_with_retry(
        devm.path,
        ["docker", "run", "-d", "--rm", "--name", "e2e-web",
         "-p", "127.0.0.1:12345:80", "nginx:alpine"],
        cwd=str(workspace.path), timeout=180,
    )
    assert run.returncode == 0, (
        f"docker run nginx failed: rc={run.returncode}\n"
        f"stderr={run.stderr.decode()!r}"
    )

    try:
        # nginx binds a moment after `docker run -d` returns. Poll for
        # up to ~15s.
        deadline = time.time() + 15
        got_code = ""
        while time.time() < deadline:
            hit = devm_exec_with_retry(
                devm.path,
                ["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
                 "--max-time", "5", "http://127.0.0.1:12345/"],
                cwd=str(workspace.path), timeout=30,
            )
            got_code = hit.stdout.decode().strip().splitlines()[-1] if hit.stdout else ""
            if got_code == "200":
                break
            time.sleep(1)
        assert got_code == "200", (
            f"guest→127.0.0.1:12345→container:80 should return 200 "
            f"(nginx default page); got {got_code!r}. Getting 000 "
            f"means the filter chain dropped the packet — user_output "
            f"rule for 172.16.0.0/12 isn't applied."
        )
    finally:
        subprocess.run(
            [devm.path, "exec", "docker", "rm", "-f", "e2e-web"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
