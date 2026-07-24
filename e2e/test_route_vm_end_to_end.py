"""Pin the fresh-install fix for `devm route vm` non-direct HTTP services.

Regression pin for the v0.9.3 bug: buildRoutes set BackendHost=127.0.0.1
and computeExposeMap filtered out non-direct services, so the daemon's
ProxyServer dialed a port nothing listened on → 502.

Post-fix flow:
  1. Guest runs an nginx container on :8080.
  2. `devm route vm` — daemon substitutes BackendHost = 127.42.0.N.
  3. curl https://api.<slug>.e2e.test/ from the Mac → 200 with nginx body.
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm

# Guest-facing port that softnet exposes and the daemon proxy dials.
# nginx:alpine listens on :80 inside the container, so the port
# mapping is HOST_PORT:CONTAINER_PORT = HOST_BACKEND_PORT:80.
HOST_BACKEND_PORT = 8080
CONTAINER_PORT = 80


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_route_vm_reaches_guest_http_service(workspace, devm, sandbox_name):
    hostname = f"api.{sandbox_name}.e2e.test"
    workspace.write_devmyaml(
        docker=True,
        services={
            "api": {"port": HOST_BACKEND_PORT, "hostname": hostname},  # not direct
        },
    )

    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=480,
    )
    assert r.returncode == 0, (
        f"devm start failed:\nstderr={r.stderr.decode()!r}"
    )

    # Run nginx in the guest. Maps host_port:container_port —
    # HOST_BACKEND_PORT is what softnet+the daemon proxy dial;
    # CONTAINER_PORT is where nginx:alpine actually listens (default 80).
    run = devm_exec_with_retry(
        devm.path,
        ["docker", "run", "-d", "--name", "nginx",
         "-p", f"{HOST_BACKEND_PORT}:{CONTAINER_PORT}",
         "nginx:alpine"],
        cwd=str(workspace.path), timeout=120,
    )
    assert run.returncode == 0, (
        f"docker run nginx failed: {run.stderr.decode()!r}"
    )

    # Apply vm-mode routes. Post-fix: daemon substitutes BackendHost.
    r = subprocess.run(
        [devm.path, "route", "vm"],
        cwd=str(workspace.path), capture_output=True, timeout=15,
    )
    assert r.returncode == 0, (
        f"devm route vm failed: {r.stderr.decode()!r}"
    )
    out = r.stdout.decode()
    assert "Routing set to vm" in out
    # CLI must show the resolved upstream (127.42.0.N:8080), not localhost
    # or 127.0.0.1.
    assert "127.42.0." in out, (
        f"CLI must print resolved 127.42.0.N upstream, got:\n{out}"
    )
    assert "localhost" not in out and "127.0.0.1" not in out, (
        f"CLI must NOT print localhost/127.0.0.1 for vm mode, got:\n{out}"
    )

    # Wait for the container to actually answer inside the guest before
    # firing Mac-side curl. `docker run -d` returns as soon as the container
    # is created, not when nginx is ready to serve — the previous
    # test-flake mode was Mac curl racing container startup and getting
    # 000 (connection failure at :443, no HTTP response).
    guest_ready_deadline = time.time() + 30
    while time.time() < guest_ready_deadline:
        probe = devm_exec_with_retry(
            devm.path,
            ["bash", "-c", f"exec 3<>/dev/tcp/127.0.0.1/{HOST_BACKEND_PORT} && echo ready"],
            cwd=str(workspace.path), timeout=10,
        )
        if probe.returncode == 0 and b"ready" in probe.stdout:
            break
        time.sleep(1)
    else:
        pytest.fail(
            f"nginx container never bound guest:{HOST_BACKEND_PORT} within 30s"
        )

    # Mac-side curl. Retry loop still present in case the daemon
    # ProxyServer + softnet listener setup lags the guest container's
    # readiness.
    deadline = time.time() + 30
    last = None
    while time.time() < deadline:
        r = subprocess.run(
            ["curl", "-sS", "-o", "/dev/null", "-w", "%{http_code}",
             "--max-time", "5", f"https://{hostname}/"],
            capture_output=True, text=True,
        )
        last = r.stdout.strip()
        if last == "200":
            break
        time.sleep(1)
    assert last == "200", (
        f"curl https://{hostname}/ expected 200, got {last!r}"
    )

    # Cleanup handled by workspace fixture (devm teardown --yes).
