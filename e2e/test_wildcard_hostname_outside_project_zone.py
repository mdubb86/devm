"""A project may declare service hostnames in zones outside its own
`<project>.<tld>` namespace — a bare exact (`links.wildprobe.test`)
or a wildcard (`*.pieces.test`) — and those hostnames must be
reachable end-to-end from the Mac and from inside the VM.

Pins the zone-ownership contract (addendum to the v0.33.0 wildcard
feature):

- Mac DNS: on project-label miss, the owning project (resolved via
  routes) answers the query.
- Guest hairpin: SNIs for zones a project owns pass the own-TLS
  termination check.
- ProxyServer: an exact route and a wildcard hit route correctly;
  the bare suffix and deeper names under a wildcard resolve to the
  owning project (so TLS succeeds) but return 502 no-route, matching
  TLS wildcard semantics.
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm

SPECIAL_PORT = 8780
CORE_PORT = 8791
CONTAINER_PORT = 80


def _curl_status(url: str, timeout: int = 5) -> str:
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
def test_wildcard_hostname_outside_project_zone(workspace, devm, sandbox_name):
    # Sandbox name is the project name. `<sandbox_name>.e2e.test` is
    # the project's own zone. The exercised hostnames live outside
    # that zone — under `pieces.e2e.test`, which is what pieceworks
    # needs for browser cross-site isolation between the links and
    # pieces domains.
    special_host = "special.pieces.e2e.test"
    wild_concrete = "k3x9p2.pieces.e2e.test"
    bare = "pieces.e2e.test"
    deeper = "a.b.pieces.e2e.test"
    wildcard = "*.pieces.e2e.test"

    workspace.write_devmyaml(
        docker={"enabled": True},
        services={
            # Exact out-of-zone hostname — covers the bare-exact case
            # (links.wildprobe.test in the user's report).
            "special": {"port": SPECIAL_PORT, "hostname": special_host},
            # Wildcard out-of-zone — covers k3x9p2.pieces.test and
            # also implicitly makes the bare + deeper names resolve
            # to this project's IP so the ProxyServer answers 502
            # rather than DNS NXDOMAIN'ing them.
            "core": {"port": CORE_PORT, "hostname": wildcard},
        },
    )

    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=480,
    )
    assert r.returncode == 0, f"devm start failed:\nstderr={r.stderr.decode()!r}"

    # Two nginx backends so a 200 from each distinguishes which
    # dispatched. Different Server headers would be ideal but nginx's
    # defaults are the same across them; just confirm status.
    for name, port in (("special", SPECIAL_PORT), ("core", CORE_PORT)):
        run = devm_exec_with_retry(
            devm.path,
            ["docker", "run", "-d", "--name", name,
             "-p", f"{port}:{CONTAINER_PORT}",
             "nginx:alpine"],
            cwd=str(workspace.path), timeout=120,
        )
        assert run.returncode == 0, (
            f"docker run {name} failed: {run.stderr.decode()!r}"
        )

    r = subprocess.run(
        [devm.path, "route", "vm"],
        cwd=str(workspace.path), capture_output=True, timeout=15,
    )
    assert r.returncode == 0, f"devm route vm failed: {r.stderr.decode()!r}"

    # Positive cases — Mac side, TLS verified against devm's CA.
    for url in (f"https://{special_host}/", f"https://{wild_concrete}/"):
        got = _poll_until_200(url, deadline_s=30)
        assert got == "200", f"curl {url} expected 200, got {got!r}"

    # Bare suffix of the wildcard: DNS resolves to the owning project
    # (so TLS handshake and HTTP request succeed to the proxy), but
    # the ProxyServer has no route for this exact name — 502 per TLS
    # wildcard semantics.
    got = _curl_status(f"https://{bare}/")
    assert got == "502", (
        f"bare {bare}/ must resolve (zone-ownership) but 502 at proxy — "
        f"got {got!r}"
    )

    # Deeper subdomain: same contract — TLS OK, no route, 502.
    got = _curl_status(f"https://{deeper}/")
    assert got == "502", (
        f"deeper {deeper}/ must resolve (zone-ownership) but 502 at proxy — "
        f"got {got!r}"
    )

    # Guest side: from inside the VM, each positive case still hits
    # nginx. The hairpin SNI gate accepts the out-of-zone SNIs via
    # the owning-project check.
    for url in (f"https://{special_host}/", f"https://{wild_concrete}/"):
        gcurl = devm_exec_with_retry(
            devm.path,
            ["bash", "-c",
             f"curl -sS -o /dev/null -w '%{{http_code}}' --max-time 5 {url}"],
            cwd=str(workspace.path), timeout=30,
        )
        assert gcurl.returncode == 0, (
            f"guest curl {url} failed: stdout={gcurl.stdout.decode()!r} "
            f"stderr={gcurl.stderr.decode()!r}"
        )
        assert gcurl.stdout.decode().strip() == "200", (
            f"guest curl {url} expected 200, got {gcurl.stdout.decode()!r}"
        )
