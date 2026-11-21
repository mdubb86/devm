"""`services.<name>.hostname: "*.foo.e2e.test"` routes any one-label
subdomain through the Mac proxy to the guest backend, matches TLS
wildcard semantics (bare suffix and deeper subdomains miss), and is
reachable from inside the VM too.

Pins the wildcard routing contract end-to-end: schema accepts the
pattern, Routes.Apply stores it, Routes.Lookup's one-label-strip
fallback hits it from both the Mac ProxyServer and the guest-origin
listener, and the per-SNI CA signs leaves for the concrete names on
demand so TLS verification succeeds.
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm

HOST_BACKEND_PORT = 8080
CONTAINER_PORT = 80


def _curl_status(url: str, timeout: int = 5) -> str:
    """curl a URL from the Mac, return the HTTP status code as a string
    ("200", "502", "000" for connect failure)."""
    r = subprocess.run(
        ["curl", "-sS", "-o", "/dev/null", "-w", "%{http_code}",
         "--max-time", str(timeout), url],
        capture_output=True, text=True,
    )
    return r.stdout.strip()


def _poll_until_200(url: str, deadline_s: int) -> str:
    deadline = time.time() + deadline_s
    last = "000"
    while time.time() < deadline:
        last = _curl_status(url)
        if last == "200":
            return last
        time.sleep(1)
    return last


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_wildcard_hostname_round_trip(workspace, devm, sandbox_name):
    # Wildcard shape: *.<glob-leaf>.<sandbox>.e2e.test. Concrete host
    # under it has the glob-leaf consumed by a single label — matches
    # TLS wildcard semantics.
    wildcard = f"*.pieces.{sandbox_name}.e2e.test"
    concrete = f"k3x9p2.pieces.{sandbox_name}.e2e.test"
    bare = f"pieces.{sandbox_name}.e2e.test"
    deeper = f"a.b.pieces.{sandbox_name}.e2e.test"

    workspace.write_devmyaml(
        docker={"enabled": True},
        services={
            "core": {"port": HOST_BACKEND_PORT, "hostname": wildcard},
        },
    )

    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=480,
    )
    assert r.returncode == 0, f"devm start failed:\nstderr={r.stderr.decode()!r}"

    # Guest backend: nginx on HOST_BACKEND_PORT, serving its default
    # page so curl can read a non-empty 200 body. The response body
    # isn't asserted — just the status — to keep the test robust to
    # nginx image changes.
    run = devm_exec_with_retry(
        devm.path,
        ["docker", "run", "-d", "--name", "core",
         "-p", f"{HOST_BACKEND_PORT}:{CONTAINER_PORT}",
         "nginx:alpine"],
        cwd=str(workspace.path), timeout=120,
    )
    assert run.returncode == 0, f"docker run nginx failed: {run.stderr.decode()!r}"

    r = subprocess.run(
        [devm.path, "route", "vm"],
        cwd=str(workspace.path), capture_output=True, timeout=15,
    )
    assert r.returncode == 0, f"devm route vm failed: {r.stderr.decode()!r}"

    # Mac side: the concrete name under the wildcard reaches nginx,
    # and TLS verifies against devm's CA (no -k).
    got = _poll_until_200(f"https://{concrete}/", deadline_s=30)
    assert got == "200", (
        f"curl https://{concrete}/ (under wildcard {wildcard}) expected 200, got {got!r}"
    )

    # The bare suffix is NOT a wildcard match. It may 502, or DNS may
    # not answer for it at all (resulting in 000 or 6). Either way, we
    # must not see 200.
    got = _curl_status(f"https://{bare}/")
    assert got != "200", (
        f"curl https://{bare}/ (bare) must not match *.X — got {got!r}"
    )

    # Deeper subdomain is NOT a one-label wildcard match.
    got = _curl_status(f"https://{deeper}/")
    assert got != "200", (
        f"curl https://{deeper}/ (deeper) must not match single-label *.X — got {got!r}"
    )

    # From inside the VM, the guest-origin listener hits the wildcard
    # route via the same one-label-strip fallback.
    gcurl = devm_exec_with_retry(
        devm.path,
        ["bash", "-c",
         f"curl -sS -o /dev/null -w '%{{http_code}}' --max-time 5 https://{concrete}/"],
        cwd=str(workspace.path), timeout=30,
    )
    assert gcurl.returncode == 0, (
        f"guest curl failed: stdout={gcurl.stdout.decode()!r} "
        f"stderr={gcurl.stderr.decode()!r}"
    )
    assert gcurl.stdout.decode().strip() == "200", (
        f"guest curl expected 200, got {gcurl.stdout.decode()!r}"
    )
