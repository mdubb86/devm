"""112: `direct: true` host-process service on a non-docker project —
the docker-vs-host-process firewall gate.

`direct: true` **host-process** service on a **non-docker** project:
`nc -lk` run as a bare VM process (via a systemd unit declared through
`exec:` — no Docker at all). Per the design's firewall gate
(`svc_ingress rule ⟺ direct && docker`), a host-process direct service
needs no forward-hook accept — its traffic never leaves the VM's own
network namespace (SSH's own path: dst is the VM itself → INPUT hook,
which devm never filters). `netcat-openbsd` isn't in the base image
(see image/provision-base.sh's package list), so this declares it via
`packages:` (same shape as test_76_packages_apt_install.py) with
`network.allow` for the Debian mirrors the apt install needs.

Kept as its own cold-start VM (not folded into test_110/112's
docker-published scenario): the `devm.yaml` topology here has no
`docker: true` at all — a genuinely different code path
(`DirectPorts` returns nil when `!cfg.Docker`), not a re-derivation of
docker-published state.

What this pins:
  - `direct` + non-docker: the service is reachable from the Mac via
    the open INPUT hook alone, but `nft list chain … svc_ingress`
    shows NO rule at all for the project (proves the gate is
    `direct && docker`, not `direct` alone).

What it doesn't cover (tested elsewhere):
  - Cold-start-time correctness (routes/nft/Caddyfile/split-horizon)
    and persistence across `devm stop`/`devm shell` for a
    direct+docker service — test_110.
  - Live add/withdraw via reconcile without a shell — test_111.
  - `direct: true` without hostname validation — test_113.

"""
from __future__ import annotations

import subprocess

import pytest

from helpers.direct import (
    BANNER,
    dig_a as _dig_a,
    dns_addr as _dns_addr,
    svc_ingress as _svc_ingress,
    vm_ip as _vm_ip,
    wait_reachable as _wait_reachable,
)

pytestmark = pytest.mark.devm

HOSTPROC_PORT = 54622      # host-process nc listen port


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
                # used for the container side in test_110/111);
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
    # ---- firewall side does). ----
    dns_host, dns_port = _dns_addr()
    answer = _dig_a(hostname, dns_host, dns_port)
    assert answer == vm_ip, (
        f"host-process direct hostname {hostname!r} should resolve "
        f"to VM IP {vm_ip!r}; got {answer!r}"
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
