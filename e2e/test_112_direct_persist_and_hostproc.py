"""112: `direct: true` persistence across `devm stop`/`devm shell`, and
the docker-vs-host-process firewall gate.

Two independent scenarios, modeled on test_91_docker.py (docker
service bring-up, reused for the container case) and the stop/restart
shape used throughout the lifecycle suite (e.g.
test_54_lifecycle_run_restarts_existing.py):

  (a) `direct + docker` service — a tiny `busybox nc` listener
      container (see test_110/111) — `devm stop` then `devm shell`
      again (VM reboots, guest state is NOT wiped — same VM, not a
      recreate). `svc_ingress` must come back from
      `/etc/nftables.d/svc_ingress.conf` (systemd's `nftables.service`
      restores it on guest boot — see the design doc's Firewall/
      Persistence section) WITHOUT a fresh `devm reconcile`/route-push
      being what re-opens the port.

  (b) `direct: true` **host-process** service on a **non-docker**
      project: `nc -lk` run as a bare VM process (via a systemd unit
      declared through `exec:` — no Docker at all). Per the design's
      firewall gate (`svc_ingress rule ⟺ direct && docker`), a
      host-process direct service needs no forward-hook accept —
      its traffic never leaves the VM's own network namespace (SSH's
      own path: dst is the VM itself → INPUT hook, which devm never
      filters). `netcat-openbsd` isn't in the base image (see
      image/provision-base.sh's package list), so this declares it
      via `packages:` (same shape as test_76_packages_apt_install.py)
      with `network.allow` for the Debian mirrors the apt install
      needs.

What this pins:
  - (a) firewall persistence: the accept rule for a direct+docker
    service survives a stop/restart cycle via the on-disk nftables
    snapshot, not just the in-memory live-apply.
  - (b) `direct` + non-docker: the service is reachable from the Mac
    via the open INPUT hook alone, but `nft list chain … svc_ingress`
    shows NO rule at all for the project (proves the gate is
    `direct && docker`, not `direct` alone).

What it doesn't cover (tested elsewhere):
  - Cold-start-time correctness (routes/nft/Caddyfile/split-horizon
    freshly provisioned) — test_110.
  - Live add/withdraw via reconcile without a shell — test_111.
  - `direct: true` without hostname validation — test_113.

KNOWN GAP (DNS): identical to test_110/111 — the Mac-side DNS
sub-assertions soft-warn-and-continue (NOT `pytest.skip`, which would
abort the whole test before reaching the reachability/nft assertions
that ARE this test's main subject) when `$DEVM_DNS_ADDR` is the
isolated lane's ephemeral `127.0.0.1:0`. See test_110's module
docstring.
"""
from __future__ import annotations

import os
import socket as _socket_module
import subprocess
import time

import pytest

from helpers import stop_and_wait_stopped
from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm

DIRECT_PORT = 54522        # (a) docker-published, declared port
CONTAINER_PORT = 9200      # (a) container's internal nc listen port
HOSTPROC_PORT = 54622      # (b) host-process nc listen port
BANNER = b"devm-direct-e2e"


def _dns_addr() -> tuple[str, int]:
    raw = os.environ.get("DEVM_DNS_ADDR", "127.0.0.1:51153")
    host, _, port_s = raw.rpartition(":")
    return (host or "127.0.0.1"), int(port_s)


def _dig_a(hostname: str, dns_host: str, dns_port: int, timeout: float = 5.0) -> str:
    r = subprocess.run(
        ["dig", "+short", "+time=2", "+tries=1",
         f"@{dns_host}", "-p", str(dns_port), hostname, "A"],
        capture_output=True, timeout=timeout,
    )
    if r.returncode != 0:
        return ""
    lines = [ln.strip() for ln in r.stdout.decode().splitlines() if ln.strip()]
    return lines[0] if lines else ""


def _tcp_connect(host: str, port: int, timeout: float = 3.0) -> bool:
    try:
        with _socket_module.create_connection((host, port), timeout=timeout):
            return True
    except OSError:
        return False


def _vm_ip(vm_name: str) -> str:
    r = subprocess.run(["tart", "ip", vm_name], capture_output=True, timeout=15)
    return r.stdout.decode().strip() if r.returncode == 0 else ""


def _svc_ingress(devm) -> str:
    r = devm_exec_with_retry(
        devm.path,
        ["sudo", "-n", "nft", "list", "chain", "inet", "devm_filter", "svc_ingress"],
        cwd=devm.cwd, timeout=30,
    )
    return r.stdout.decode() if r.returncode == 0 else ""


def _wait_reachable(host: str, port: int, timeout: float = 40.0) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        if _tcp_connect(host, port, timeout=3):
            return True
        time.sleep(1)
    return False


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_direct_docker_persists_across_stop_shell(workspace, devm, sandbox_name):
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

    vm_ip_before = _vm_ip(workspace.vm_name)
    assert vm_ip_before, "could not get VM IP via `tart ip`"

    # `--restart unless-stopped` (not `--rm`) so the container survives
    # and re-launches after the guest's docker daemon comes back up
    # post-restart — `--rm` and `--restart` are mutually exclusive.
    run = devm_exec_with_retry(
        devm.path,
        ["docker", "run", "-d", "--restart", "unless-stopped",
         "--name", "e2e-direct-persist-nc",
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
        # Sanity baseline before the stop/restart cycle.
        assert _wait_reachable(vm_ip_before, DIRECT_PORT), (
            f"baseline: {vm_ip_before}:{DIRECT_PORT} should be reachable "
            f"before the stop/restart cycle"
        )
        assert f"ct original proto-dst {DIRECT_PORT} accept" in _svc_ingress(devm), (
            "baseline: svc_ingress missing the accept rule before the "
            "stop/restart cycle"
        )

        # `devm stop` then `devm shell` again — same VM (not a
        # recreate), Tart may hand out a new DHCP lease on reboot.
        stop_and_wait_stopped(devm, sandbox_name)

        reshell = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert reshell.returncode == 0, (
            f"devm shell (restart existing VM) failed:\n"
            f"stderr={reshell.stderr.decode()!r}"
        )

        vm_ip_after = _vm_ip(workspace.vm_name)
        assert vm_ip_after, "could not get VM IP after restart"

        # ---- Assertion: svc_ingress restored from
        # ---- /etc/nftables.d/svc_ingress.conf on guest boot — no
        # ---- fresh reconcile/route-push involved, just `devm shell`'s
        # ---- normal /vm/start path. ----
        deadline = time.time() + 30
        nft_out = ""
        while time.time() < deadline:
            nft_out = _svc_ingress(devm)
            if f"proto-dst {DIRECT_PORT}" in nft_out:
                break
            time.sleep(1)
        assert f"ct original proto-dst {DIRECT_PORT} accept" in nft_out, (
            f"svc_ingress not restored after stop/shell cycle:\n{nft_out}"
        )

        # ---- Assertion: DNS answers the (possibly NEW) VM IP. Soft
        # ---- warn-and-continue (see module docstring KNOWN GAP) —
        # ---- must NOT abort before the reachability check below. ----
        dns_host, dns_port = _dns_addr()
        if dns_port == 0:
            print(
                "WARNING: DEVM_DNS_ADDR is ephemeral; skipping the "
                "Mac-side DNS sub-assertion only (see module docstring "
                "KNOWN GAP)."
            )
        else:
            answer = _dig_a(hostname, dns_host, dns_port)
            assert answer == vm_ip_after, (
                f"after stop/shell, DNS should answer the current VM "
                f"IP {vm_ip_after!r} for {hostname!r}; got {answer!r}"
            )

        # ---- Assertion: still reachable (container came back via its
        # ---- restart policy once docker re-enabled post-boot). ----
        assert _wait_reachable(vm_ip_after, DIRECT_PORT, timeout=60), (
            f"{vm_ip_after}:{DIRECT_PORT} not reachable after stop/shell "
            f"cycle — svc_ingress or the container's restart policy "
            f"didn't recover"
        )
    finally:
        subprocess.run(
            [devm.path, "exec", "docker", "rm", "-f", "e2e-direct-persist-nc"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )


@pytest.mark.slow
@pytest.mark.timeout(400)
def test_direct_host_process_no_docker_no_svc_ingress_rule(workspace, devm, sandbox_name):
    hostname = f"{sandbox_name}-web.test"
    # NOTE: no `docker: true` — this project never runs a container, so
    # the direct service below is a bare VM (host) process. `nc` isn't
    # in the base image; declare it via `packages:` (test_76's shape),
    # which needs the Debian mirrors allow-listed for the apt install
    # to reach them through iron-proxy.
    workspace.write_devmyaml(
        packages=["netcat-openbsd"],
        network={"allow": ["deb.debian.org", "security.debian.org"]},
        services={
            "web": {
                "port": HOSTPROC_PORT,
                "hostname": hostname,
                "direct": True,
                # `-k` keeps nc listening for repeat connections itself
                # (no while-loop wrapper needed, unlike the plain `-l`
                # used for the container side in test_110/111/112a);
                # `restart: always` covers the case where it exits.
                "exec": ["sh", "-c",
                         f"printf '%s' '{BANNER.decode()}' | "
                         f"nc -lk -p {HOSTPROC_PORT}"],
                "restart": "always",
            },
        },
    )

    shell = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert shell.returncode == 0, (
        f"devm shell cold-start failed:\nstderr={shell.stderr.decode()!r}"
    )

    vm_ip = _vm_ip(workspace.vm_name)
    assert vm_ip, "could not get VM IP via `tart ip`"

    # ---- Assertion: DNS answers VM_IP for a host-process direct
    # ---- service exactly like a container-published one — the DNS
    # ---- side doesn't distinguish docker vs host-process (only the
    # ---- firewall side does). Soft warn-and-continue (see module
    # ---- docstring KNOWN GAP). ----
    dns_host, dns_port = _dns_addr()
    if dns_port != 0:
        answer = _dig_a(hostname, dns_host, dns_port)
        assert answer == vm_ip, (
            f"host-process direct hostname {hostname!r} should resolve "
            f"to VM IP {vm_ip!r}; got {answer!r}"
        )
    else:
        print(
            "WARNING: DEVM_DNS_ADDR is ephemeral; skipping the Mac-side "
            "DNS sub-assertion only (see module docstring KNOWN GAP)."
        )

    # ---- Assertion: reachable from the Mac via the open INPUT hook —
    # ---- no forward-hook accept needed (SSH's own path). ----
    assert _wait_reachable(vm_ip, HOSTPROC_PORT), (
        f"host-process direct service should be reachable at "
        f"{vm_ip}:{HOSTPROC_PORT} via the VM's open INPUT hook"
    )

    # ---- Assertion: svc_ingress carries NO rule for this port (or
    # ---- any port) — proves the gate is `direct && docker`, not
    # ---- `direct` alone. A non-docker project's chain must be
    # ---- entirely free of `ct original proto-dst` accepts. ----
    nft_out = _svc_ingress(devm)
    assert f"proto-dst {HOSTPROC_PORT}" not in nft_out, (
        f"svc_ingress must have NO rule for a host-process direct "
        f"service on a non-docker project:\n{nft_out}"
    )
    assert "ct original proto-dst" not in nft_out, (
        f"svc_ingress should be entirely empty on a non-docker "
        f"project (DirectPorts returns nil when !cfg.Docker):\n{nft_out}"
    )
