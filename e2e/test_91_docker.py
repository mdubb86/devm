"""91: docker as first-class devm feature.

Consolidated end-to-end proof of `docker: true`:
  - Docker Engine installs and `docker info` works without sudo
    (recipe-side ExecStartPost chmod is now devm-owned).
  - Container HTTPS to an allow-listed host succeeds WITHOUT -k —
    proves devm-runc-shim bind-mounted the guest CA bundle and the
    container's TLS client trusted iron-proxy's MITM cert.
  - Container HTTPS to a NON-allow-listed host gets 403 from
    iron-proxy (not 000 / timeout).
  - Host process on the guest reaches a container's published port
    on 127.0.0.1:<non-80/443>. Pins the 172.16.0.0/12 user_output
    rule.

Single test — cold-start is ~5 min; splitting triples cost for no
coverage gain.
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_docker_first_class_end_to_end(workspace, devm):
    workspace.write_devmyaml(
        docker=True,
        network={
            "allow": [
                # Test target — httpbin is a stable HTTP-test service.
                # -A "Mozilla/5.0" sidesteps its 503-on-default-curl-UA.
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

    # ---- Assertion 2: container HTTPS to an allow-listed host succeeds
    # ---- WITHOUT -k. This is the key positive assertion the shim
    # ---- unlocks — container's TLS client MUST trust the mounted
    # ---- devm CA bundle.
    allowed = devm_exec_with_retry(
        devm.path,
        ["docker", "run", "--rm", "curlimages/curl:latest",
         "-s", "-o", "/dev/null", "-w", "%{http_code}",
         "-A", "Mozilla/5.0",
         "--max-time", "15", "https://httpbin.org/status/200"],
        cwd=str(workspace.path), timeout=180,
    )
    assert allowed.returncode == 0, (
        f"allow-listed container HTTPS failed: rc={allowed.returncode}\n"
        f"stdout={allowed.stdout.decode()!r}\n"
        f"stderr={allowed.stderr.decode()!r}\n"
        f"rc=60 with 000 typically means container's TLS client did "
        f"not trust the mounted CA bundle."
    )
    got_code = allowed.stdout.decode().strip().splitlines()[-1]
    assert got_code == "200", (
        f"expected 200 from httpbin.org via container (real cert-"
        f"verified TLS to iron-proxy MITM); got {got_code!r}"
    )

    # ---- Assertion 3: container HTTPS to a BLOCKED host returns 403,
    # ---- not 000/timeout. 403 = packet reached iron-proxy's app
    # ---- layer; 000 = escaped through Docker's forward hook.
    blocked = devm_exec_with_retry(
        devm.path,
        ["docker", "run", "--rm", "curlimages/curl:latest",
         "-s", "-o", "/dev/null", "-w", "%{http_code}",
         "-A", "Mozilla/5.0",
         "--max-time", "15", "https://google.com/"],
        cwd=str(workspace.path), timeout=180,
    )
    assert blocked.returncode == 0, (
        f"curl itself failed at network layer, not HTTP layer — "
        f"inconclusive. stderr={blocked.stderr.decode()!r}"
    )
    got_code = blocked.stdout.decode().strip().splitlines()[-1]
    assert got_code == "403", (
        f"non-allow-listed google.com from container should return 403 "
        f"(iron-proxy reject); got {got_code!r}. Getting 000 means "
        f"container egress bypassed iron-proxy entirely."
    )

    # ---- Assertion 4: host → container reachability on published port. ----
    #
    # Non-80/443 port on the host side — the 80/443 hijack by devm_nat's
    # OUTPUT DNAT is a known gap (deferred; needs a user_nat chain).
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
        # nginx binds a moment after `docker run -d` returns. Poll up to ~15s.
        deadline = time.time() + 15
        got_code = ""
        while time.time() < deadline:
            hit = devm_exec_with_retry(
                devm.path,
                ["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
                 "--max-time", "5", "http://127.0.0.1:12345/"],
                cwd=str(workspace.path), timeout=30,
            )
            if hit.returncode != 0:
                print(
                    f"poll: curl rc={hit.returncode} "
                    f"stderr={hit.stderr.decode()!r} — retrying"
                )
                time.sleep(1)
                continue
            got_code = hit.stdout.decode().strip().splitlines()[-1] if hit.stdout else ""
            if got_code == "200":
                break
            time.sleep(1)
        assert got_code == "200", (
            f"guest→127.0.0.1:12345→container:80 should return 200 "
            f"(nginx default page); got {got_code!r}. 000 means the "
            f"filter chain dropped the packet — 172.16.0.0/12 user_output "
            f"rule is missing."
        )
    finally:
        subprocess.run(
            [devm.path, "exec", "docker", "rm", "-f", "e2e-web"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
