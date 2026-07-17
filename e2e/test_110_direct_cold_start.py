"""110: `direct: true` cold-start — split-horizon reachability for a
raw-TCP service.

Modeled on test_91_docker.py — reuses its already-proven docker-in-VM
scaffold (`docker: true`, `devm exec docker run`,
`helpers.exec_retry.devm_exec_with_retry`) — and test_37_route_vm.py
(the daemon's Unix-socket `/routes` HTTP probe). A real CONTAINER is
required here (not a host process): only a container-published port
traverses the `forward` hook that `svc_ingress` guards, so this is the
only way to actually exercise the firewall rule end to end.

Uses a tiny `busybox` container running a persistent `nc -l -p <port>`
listener instead of Postgres — cheap to pull, boots instantly, and the
banner it emits on connect is enough to prove real data flow through
the whole path (Mac → firewall → Docker DNAT → container), without
needing a real database client. Declares a `docker: true` project with
one `direct: true` service, cold-starts via `devm shell`, publishes
the container on the service's declared port, then walks every leg of
the design doc's "resulting model" table:

  Mac → VM_IP:<port> → firewall (`svc_ingress`) → container   (direct)
  VM  → 127.0.0.1:<port> → loopback container                 (unchanged)

What this pins:
  - the daemon's `GET /routes` shows the hostname with `"direct": true`
    (no `backend_host` — direct routes carry no dial target);
  - a Mac-side DNS query for the hostname resolves the VM's current
    `tart ip`, NOT `127.0.0.1`;
  - `nft list chain inet devm_filter svc_ingress` inside the VM
    contains a conntrack-original accept for the declared (pre-DNAT)
    port — the container's actual listening port never appears;
  - the in-VM `/etc/caddy/Caddyfile` has NO block for the hostname —
    direct services are never HTTP-fronted;
  - `<vm_ip>:<port>` is reachable from the Mac AND the banner the
    container emits round-trips correctly (proves the firewall accept
    + Docker's prerouting DNAT let an EXTERNAL connection's data
    through — not just guest-local traffic);
  - the identical port, with the identical banner, is ALSO reachable
    from inside the VM via 127.0.0.1 (split-horizon: same URL works on
    both planes).

What it doesn't cover (tested elsewhere):
  - Live add/withdraw via reconcile without a shell — test_111.
  - Persistence across `devm stop`/reboot and the docker-vs-host-process
    firewall gate — test_112.
  - The `direct: true` + no-hostname validation error — test_113 (no
    VM needed there).

KNOWN GAP (see task-11-report.md): the Mac-side DNS assertion needs a
non-ephemeral `$DEVM_DNS_ADDR`. The isolated e2e lane's daemon binds
`127.0.0.1:0` (run.sh) and nothing exposes the OS-picked port back to
a test process (unlike the NTP server's picked-port pattern in
internal/serviceapi/ntp.go — the DNS server has no equivalent), so
that one sub-assertion self-skips under `E2E_ISOLATE=1` and only runs
when `$DEVM_DNS_ADDR` is a fixed, queryable port (e.g. the real
installed daemon's default `:51153`, `E2E_ISOLATE=0`).
"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers.direct import (
    BANNER,
    dig_a as _dig_a,
    dns_addr as _dns_addr,
    get_routes as _get_routes,
    tcp_read_banner as _tcp_read_banner,
    vm_ip as _vm_ip,
)
from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm

# The service's DECLARED (pre-DNAT) port — what devm.yaml, /routes,
# and the svc_ingress conntrack-original match all use.
DIRECT_PORT = 54322
# The container's INTERNAL listening port — must never appear in the
# svc_ingress chain (which matches the pre-DNAT port only).
CONTAINER_PORT = 9000


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_direct_cold_start_split_horizon(workspace, devm, sandbox_name):
    hostname = f"{sandbox_name}-nc.test"
    workspace.write_devmyaml(
        docker=True,
        services={
            "nc": {"port": DIRECT_PORT, "hostname": hostname, "direct": True},
        },
    )

    shell = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=480,
    )
    assert shell.returncode == 0, (
        f"devm shell cold-start failed:\nstderr={shell.stderr.decode()!r}"
    )

    project_id = workspace.slug
    vm_ip = _vm_ip(workspace.vm_name)
    assert vm_ip, "could not get VM IP via `tart ip`"

    # ---- Assertion 1: GET /routes shows the hostname, marked direct,
    # ---- with no backend_host (direct routes carry no dial target). ----
    routes = _get_routes()
    assert project_id in routes, f"no /routes entry for {project_id!r}: {routes}"
    entry = next((e for e in routes[project_id] if e["hostname"] == hostname), None)
    assert entry is not None, (
        f"{hostname!r} missing from routes: {routes[project_id]}"
    )
    assert entry.get("direct") is True, (
        f"route for {hostname!r} not marked direct: {entry}"
    )
    assert not entry.get("backend_host"), (
        f"direct route for {hostname!r} should carry no backend_host: {entry}"
    )

    # ---- Assertion 2: Mac-side DNS answers the VM's IP, not 127.0.0.1. ----
    # NOTE: uses a soft warn-and-continue, NOT pytest.skip() — skipping
    # here would abort the WHOLE test and never reach the nft/
    # reachability assertions below, which are this test's main
    # subject and don't depend on DNS at all.
    dns_host, dns_port = _dns_addr()
    if dns_port == 0:
        print(
            "WARNING: DEVM_DNS_ADDR is ephemeral (127.0.0.1:0) in the "
            "isolated e2e lane; the daemon's *.test resolver doesn't "
            "expose its OS-picked UDP port anywhere queryable from a "
            "test process (see module docstring KNOWN GAP). Skipping "
            "the Mac-side DNS sub-assertion only; continuing with "
            "nft/reachability checks against the VM IP directly."
        )
    else:
        answer = _dig_a(hostname, dns_host, dns_port)
        assert answer == vm_ip, (
            f"expected DNS to answer the VM IP {vm_ip!r} for direct "
            f"hostname {hostname!r}; got {answer!r}"
        )

    # ---- Bring up a tiny busybox `nc` listener, published on the
    # ---- service's DECLARED port. Bare `-p PORT:CONTAINER_PORT` binds
    # ---- 0.0.0.0 (Docker's default) — required so a Mac→VM_IP
    # ---- connection (not just guest-local traffic) hits Docker's
    # ---- prerouting DNAT. The while-loop re-serves the banner on every
    # ---- new connection (busybox nc exits after one client). ----
    run = devm_exec_with_retry(
        devm.path,
        ["docker", "run", "-d", "--rm", "--name", "e2e-direct-nc",
         "-p", f"{DIRECT_PORT}:{CONTAINER_PORT}",
         "busybox", "sh", "-c",
         f"while true; do printf '%s' '{BANNER.decode()}' | "
         f"nc -l -p {CONTAINER_PORT}; done"],
        cwd=str(workspace.path), timeout=120,
    )
    assert run.returncode == 0, (
        f"docker run busybox nc failed: rc={run.returncode}\n"
        f"stderr={run.stderr.decode()!r}"
    )

    try:
        # ---- Assertion 3: svc_ingress carries the conntrack-original
        # ---- accept for the DECLARED port, not the container's
        # ---- internal listening port. ----
        deadline = time.time() + 30
        nft_out = ""
        while time.time() < deadline:
            nft = devm_exec_with_retry(
                devm.path,
                ["sudo", "-n", "nft", "list", "chain", "inet", "devm_filter", "svc_ingress"],
                cwd=str(workspace.path), timeout=30,
            )
            nft_out = nft.stdout.decode()
            if f"proto-dst {DIRECT_PORT}" in nft_out:
                break
            time.sleep(1)
        assert f"ct original proto-dst {DIRECT_PORT} accept" in nft_out, (
            f"svc_ingress chain missing conntrack-original accept for "
            f"port {DIRECT_PORT}:\n{nft_out}"
        )
        assert str(CONTAINER_PORT) not in nft_out, (
            f"svc_ingress should match the pre-DNAT declared port "
            f"({DIRECT_PORT}), never the container's internal port "
            f"({CONTAINER_PORT}):\n{nft_out}"
        )

        # ---- Assertion 4: the in-VM Caddyfile has NO block for the
        # ---- direct hostname. ----
        caddyfile = devm_exec_with_retry(
            devm.path, ["cat", "/etc/caddy/Caddyfile"],
            cwd=str(workspace.path), timeout=30,
        )
        assert caddyfile.returncode == 0, (
            f"reading in-VM Caddyfile failed: {caddyfile.stderr.decode()!r}"
        )
        assert hostname not in caddyfile.stdout.decode(), (
            f"direct service {hostname!r} must not get a Caddy block:\n"
            f"{caddyfile.stdout.decode()}"
        )

        # ---- Assertion 5: reachable from the Mac at VM_IP:port, AND
        # ---- the banner round-trips — proves the firewall accept +
        # ---- Docker's forward-hook DNAT actually carry real data
        # ---- through, not just a bare SYN/ACK. ----
        deadline = time.time() + 30
        got = None
        while time.time() < deadline:
            got = _tcp_read_banner(vm_ip, DIRECT_PORT, BANNER, timeout=3)
            if got == BANNER:
                break
            time.sleep(1)
        assert got == BANNER, (
            f"Mac could not read the expected banner from "
            f"{vm_ip}:{DIRECT_PORT} (got {got!r}) — svc_ingress accept "
            f"or Docker's forward-hook DNAT is not letting the "
            f"connection through"
        )

        # ---- Assertion 6: split-horizon — the SAME port, with the
        # ---- SAME banner, is reachable from INSIDE the VM via
        # ---- loopback. `/dev/tcp` is a bash builtin (base image has
        # ---- bash — test_50), so this needs no extra installed
        # ---- tooling. ----
        in_vm = devm_exec_with_retry(
            devm.path,
            ["bash", "-c",
             f"timeout 5 bash -c "
             f"'exec 3<>/dev/tcp/127.0.0.1/{DIRECT_PORT}; "
             f"head -c {len(BANNER)} <&3'"],
            cwd=str(workspace.path), timeout=30,
        )
        assert in_vm.returncode == 0 and in_vm.stdout == BANNER, (
            f"in-VM loopback 127.0.0.1:{DIRECT_PORT} did not return the "
            f"expected banner: rc={in_vm.returncode} "
            f"stdout={in_vm.stdout!r} stderr={in_vm.stderr.decode()!r}"
        )
    finally:
        subprocess.run(
            [devm.path, "exec", "docker", "rm", "-f", "e2e-direct-nc"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
