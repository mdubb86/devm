"""110: `direct: true` cold-start — split-horizon reachability for a
raw-TCP service, plus persistence across `devm stop`/`devm shell`.

Modeled on test_91_docker.py — reuses its already-proven docker-in-VM
scaffold (`docker: true`, `devm exec docker run`,
`helpers.exec_retry.devm_exec_with_retry`) — and test_37_route_vm.py
(the daemon's Unix-socket `/routes` HTTP probe). A real CONTAINER is
required here (not a host process): only a container-published port
traverses Docker's own forward-hook DNAT, so this is the only way to
actually exercise softnet's direct-service ingress path end to end.

Uses a tiny `busybox` container running a persistent `nc -l -p <port>`
listener instead of Postgres — cheap to pull, boots instantly, and the
banner it emits on connect is enough to prove real data flow through
the whole path (Mac → softnet → Docker DNAT → container), without
needing a real database client. Declares a `docker: true` project with
one `direct: true` service, cold-starts via `devm shell`, publishes
the container on the service's declared port, then walks every leg of
the design doc's "resulting model" table:

  Mac → pool IP:<port> → softnet's direct listener → container  (direct)
  VM  → 127.0.0.1:<port> → loopback container                   (unchanged)

...then continues in the SAME boot into a `devm stop` + `devm shell`
restart cycle on the same container (`--restart unless-stopped`, not
`--rm`, so it survives) to pin reachability persistence.

What this pins:
  - the daemon's `GET /routes` shows the hostname with `"direct": true`
    (no `backend_host` — direct routes carry no dial target);
  - a Mac-side DNS query for the hostname resolves the project's pool
    IP (`127.42.0.N`, the single Mac-side address for both direct and
    non-direct services post-B3), NOT `127.0.0.1`;
  - the in-VM `/etc/caddy/Caddyfile` has NO block for the hostname —
    direct services are never HTTP-fronted;
  - `<pool_ip>:<port>` is reachable from the Mac AND the banner the
    container emits round-trips correctly (proves the firewall accept
    + Docker's prerouting DNAT let an EXTERNAL connection's data
    through — not just guest-local traffic);
  - the identical port, with the identical banner, is ALSO reachable
    from inside the VM via 127.0.0.1 (split-horizon: same URL works on
    both planes);
  - after `devm stop` + `devm shell` (VM reboot, same VM, not a
    recreate), the service is still reachable at the same pool IP
    (container came back via its `--restart unless-stopped` policy
    once docker re-enabled post-boot).

What it doesn't cover (tested elsewhere):
  - Live add/withdraw via reconcile without a shell — test_111.
  - The docker-vs-host-process firewall gate (`direct` + non-docker
    project needs NO svc_ingress rule at all) — test_112, a genuinely
    different `devm.yaml` topology (no docker, host-process `exec:`
    service) that can't be folded in here without losing that
    boundary's own coverage.
  - The `direct: true` + no-hostname validation error — test_113 (no
    VM needed there).

"""
from __future__ import annotations

import subprocess
import time

import pytest

from helpers import pool_ip, stop_and_wait_stopped
from helpers.direct import (
    BANNER,
    dig_a as _dig_a,
    dns_addr as _dns_addr,
    get_routes as _get_routes,
    tcp_read_banner as _tcp_read_banner,
    wait_reachable as _wait_reachable,
)
from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm

# The service's DECLARED (pre-DNAT) port — what devm.yaml, /routes,
# and the pool_ip:port reachability checks below all use.
DIRECT_PORT = 54322
# The container's INTERNAL listening port — never exposed directly;
# Docker's own prerouting DNAT maps DIRECT_PORT to this.
CONTAINER_PORT = 9000


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_direct_cold_start_split_horizon_and_persist(workspace, devm, sandbox_name):
    hostname = f"nc.{sandbox_name}.e2e.test"
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
    pool = pool_ip(project_id)

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

    # ---- Assertion 2: Mac-side DNS answers the project's pool IP. ----
    dns_host, dns_port = _dns_addr()
    answer = _dig_a(hostname, dns_host, dns_port)
    assert answer == pool, (
        f"expected DNS to answer the pool IP {pool!r} for direct "
        f"hostname {hostname!r}; got {answer!r}"
    )

    # ---- Bring up a tiny busybox `nc` listener, published on the
    # ---- service's DECLARED port. Bare `-p PORT:CONTAINER_PORT` binds
    # ---- 0.0.0.0 (Docker's default) — required so a Mac→VM_IP
    # ---- connection (not just guest-local traffic) hits Docker's
    # ---- prerouting DNAT. The while-loop re-serves the banner on every
    # ---- new connection (busybox nc exits after one client). ----
    # `--restart unless-stopped` (not `--rm`) so the container survives
    # and re-launches after the guest's docker daemon comes back up
    # post-restart, for the stop/shell persistence phase below — `--rm`
    # and `--restart` are mutually exclusive.
    run = devm_exec_with_retry(
        devm.path,
        ["docker", "run", "-d", "--restart", "unless-stopped",
         "--name", "e2e-direct-nc",
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
        # ---- the banner round-trips — proves softnet's Mac-side direct
        # ---- listener + Docker's forward-hook DNAT actually carry real
        # ---- data through, not just a bare SYN/ACK. ----
        deadline = time.time() + 30
        got = None
        while time.time() < deadline:
            got = _tcp_read_banner(pool, DIRECT_PORT, BANNER, timeout=3)
            if got == BANNER:
                break
            time.sleep(1)
        assert got == BANNER, (
            f"Mac could not read the expected banner from "
            f"{pool}:{DIRECT_PORT} (got {got!r}) — softnet's direct "
            f"listener or Docker's forward-hook DNAT is not letting the "
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

        # ---- Persistence phase (test_112a): `devm stop` then
        # ---- `devm shell` again — same VM (not a recreate), Tart may
        # ---- hand out a new DHCP lease on reboot. Continues in this
        # ---- same boot rather than a fresh cold-start, since the
        # ---- baseline state it needs (reachable container) was
        # ---- already established by assertions 4-6 above. ----
        stop_and_wait_stopped(devm, sandbox_name)

        reshell = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert reshell.returncode == 0, (
            f"devm shell (restart existing VM) failed:\n"
            f"stderr={reshell.stderr.decode()!r}"
        )

        # ---- Assertion: still reachable (container came back via its
        # ---- restart policy once docker re-enabled post-boot). ----
        assert _wait_reachable(pool, DIRECT_PORT, timeout=60), (
            f"{pool}:{DIRECT_PORT} not reachable after stop/shell "
            f"cycle — softnet's direct listener or the container's "
            f"restart policy didn't recover"
        )
    finally:
        subprocess.run(
            [devm.path, "exec", "docker", "rm", "-f", "e2e-direct-nc"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
