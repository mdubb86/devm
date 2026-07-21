"""111: `direct: true` live add + live withdraw via `devm reconcile` —
no shell, no teardown, no VM bounce.

Modeled on test_92_iron_proxy_reconcile.py (edit devm.yaml → `devm
reconcile` → assert new behavior live, on the SAME running VM,
tart-PID-unchanged bounce proof) crossed with test_37_route_vm.py's
`/routes` probe, and reuses test_91_docker.py's docker-in-VM scaffold.
The design doc's `KindServiceDirectChange` bucket is `BucketLive` with
a real `ApplyLive` path (NOT the "classified live but silently
deferred to next cold-start" trap `KindPortAdd` falls into) — this
test is the proof that flipping `direct` takes effect on the spot.

A real CONTAINER is required (not a host process): only a
container-published port traverses the `forward` hook that
`svc_ingress` guards. A tiny `busybox nc` listener (see test_110) is
started ONCE, published on the service's declared port, and left
running for the whole test — the thing that changes across reconciles
is purely the firewall/DNS/route state around it, isolating the
assertions to the live-apply path itself.

Sequence:
  1. `devm start` with `docker: true` + a service that has a hostname
     but `direct: false` (proxied — the default). Baseline: DNS
     answers the project's pool IP (`127.42.0.N`), no `svc_ingress`
     rule for the port.
  2. Start the `nc` listener container, published at the declared port.
  3. Flip `direct: true`, `devm reconcile` (no shell). Assert: DNS
     still answers the pool IP (unchanged — post-B3 DNS never
     distinguishes direct from non-direct); `svc_ingress` carries the
     accept; the port's banner is readable from the Mac.
  4. Flip back to `direct: false`, `devm reconcile`. Assert: DNS still
     answers the pool IP; the `svc_ingress` rule is gone; the port is
     NO LONGER reachable from the Mac (forward hook's default-drop
     re-applies once the accept is withdrawn) — the direct/non-direct
     signal lives in nftables + reachability, not DNS.

Throughout, the VM's `tart run` PID is checked unchanged (mirrors
test_92's bounce-proof) — direct changes must never recreate the VM.

What this pins:
  - Live add: `svc_ingress` flush-rebuild + route re-push + DNS answer
    change, all without `devm shell`/teardown.
  - Live withdraw: the SAME machinery in reverse — DNS reverts, the
    accept rule is gone, and (crucially) the port actually stops being
    reachable, not just that the config LOOKS reverted.
  - No VM bounce for either direction.

What it doesn't cover (tested elsewhere):
  - Cold-start-time correctness (routes/nft/Caddyfile/split-horizon
    freshly provisioned) — test_110.
  - Persistence across `devm stop`/reboot and the docker-vs-host-process
    firewall gate — test_112.
  - `direct: true` without hostname validation — test_113.

KNOWN GAP (reconcile stdout): `internal/orchestrator/format.go`'s
`formatChange`/`changeKindJSON` switches enumerate every other
`reconcile.Kind*` constant but have NO case for
`KindServiceDirectChange` — a direct flip currently renders as the
literal string `"(unknown change)"` in `devm reconcile` output (text
mode) and `"unknown"` (JSON mode), even though the underlying
live-apply (this test's real subject) works. Filed as a follow-up;
this test does not assert on `reconcile` stdout text for that reason —
only on functional behavior (DNS/nft/reachability).
"""
from __future__ import annotations

import subprocess
import time

import pytest
import yaml

from helpers import pool_ip
from helpers.direct import (
    BANNER,
    dig_a as _dig_a,
    dns_addr as _dns_addr,
    get_routes as _get_routes,
    svc_ingress as _svc_ingress,
    tcp_connect as _tcp_connect,
    tcp_read_banner as _tcp_read_banner,
)
from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm

# Distinct from test_110's ports to avoid any port bleed if tests
# happen to run concurrently against distinct VMs.
DIRECT_PORT = 54422
CONTAINER_PORT = 9100


def _tart_pid(vm_name: str) -> int | None:
    out = subprocess.run(
        ["pgrep", "-f", f"tart run.*{vm_name}"],
        capture_output=True, text=True,
    )
    if out.returncode != 0:
        return None
    pids = [line for line in out.stdout.strip().splitlines() if line]
    return int(pids[0]) if pids else None


def _set_direct(workspace, devm, value: bool) -> None:
    # devm.yaml is host-immutable (config-lock) while the VM runs; unlock
    # before editing. The `devm reconcile` each caller runs right after
    # this re-locks it (unlock -> edit -> reconcile always ends locked,
    # per test_120_config_lock.py), so each call needs its own unlock.
    devm.unlock()
    cfg = yaml.safe_load(workspace.devmyaml_path.read_text())
    cfg["services"]["nc"]["direct"] = value
    workspace.devmyaml_path.write_text(yaml.safe_dump(cfg, sort_keys=False))


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_direct_live_add_and_withdraw(workspace, devm, sandbox_name):
    hostname = f"{sandbox_name}-nc.e2e.test"
    workspace.write_devmyaml(
        docker=True,
        services={
            "nc": {"port": DIRECT_PORT, "hostname": hostname, "direct": False},
        },
    )

    start = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=480,
    )
    assert start.returncode == 0, (
        f"devm start failed:\nstderr={start.stderr.decode()!r}"
    )

    project_id = workspace.slug
    pool = pool_ip(project_id)
    pid_before = _tart_pid(workspace.vm_name)
    assert pid_before is not None, "expected a running tart process for the VM"

    dns_host, dns_port = _dns_addr()

    # ---- Baseline (direct: false): DNS answers the project's pool IP
    # ---- (same as direct — post-B3 there's no DNS-answer difference
    # ---- between the two modes); no svc_ingress rule for this port
    # ---- yet. ----
    baseline = _dig_a(hostname, dns_host, dns_port)
    assert baseline == pool, (
        f"non-direct hostname {hostname!r} should resolve to the "
        f"project's pool IP {pool!r} before any direct flip; got "
        f"{baseline!r}"
    )
    assert f"proto-dst {DIRECT_PORT}" not in _svc_ingress(devm), (
        "svc_ingress should have no rule for this port before any "
        "direct flip"
    )

    # ---- Bring up the `nc` listener container ONCE, published at the
    # ---- declared port. Stays up for the rest of the test — only the
    # ---- firewall/DNS/route state around it changes. ----
    run = devm_exec_with_retry(
        devm.path,
        ["docker", "run", "-d", "--rm", "--name", "e2e-direct-live-nc",
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
        # ================= LIVE ADD =================
        _set_direct(workspace, devm, True)
        reconcile = devm.reconcile(yes=True, timeout=120)
        assert reconcile.returncode == 0, (
            f"devm reconcile (flip to direct) failed:\n"
            f"stdout={reconcile.stdout.decode()!r}\n"
            f"stderr={reconcile.stderr.decode()!r}"
        )

        routes = _get_routes()
        entry = next(
            (e for e in routes.get(project_id, []) if e["hostname"] == hostname),
            None,
        )
        assert entry is not None and entry.get("direct") is True, (
            f"route for {hostname!r} not marked direct after reconcile: "
            f"{routes.get(project_id)}"
        )

        answer = _dig_a(hostname, dns_host, dns_port)
        assert answer == pool, (
            f"after live direct-add, DNS should answer the pool IP "
            f"{pool!r} for {hostname!r}; got {answer!r}"
        )

        deadline = time.time() + 30
        nft_out = ""
        while time.time() < deadline:
            nft_out = _svc_ingress(devm)
            if f"proto-dst {DIRECT_PORT}" in nft_out:
                break
            time.sleep(1)
        assert f"ct original proto-dst {DIRECT_PORT} accept" in nft_out, (
            f"svc_ingress missing accept for port {DIRECT_PORT} after "
            f"live direct-add:\n{nft_out}"
        )

        deadline = time.time() + 30
        got = None
        while time.time() < deadline:
            got = _tcp_read_banner(pool, DIRECT_PORT, BANNER, timeout=3)
            if got == BANNER:
                break
            time.sleep(1)
        assert got == BANNER, (
            f"Mac could not read the expected banner from "
            f"{pool}:{DIRECT_PORT} after live direct-add (got {got!r}) "
            f"— expected reachable WITHOUT a shell/teardown"
        )

        assert _tart_pid(workspace.vm_name) == pid_before, (
            "VM was bounced by a live direct-add reconcile; it must "
            "apply live only"
        )

        # ================= LIVE WITHDRAW =================
        _set_direct(workspace, devm, False)
        reconcile = devm.reconcile(yes=True, timeout=120)
        assert reconcile.returncode == 0, (
            f"devm reconcile (flip back) failed:\n"
            f"stdout={reconcile.stdout.decode()!r}\n"
            f"stderr={reconcile.stderr.decode()!r}"
        )

        answer = _dig_a(hostname, dns_host, dns_port)
        assert answer == pool, (
            f"after live direct-withdraw, DNS should still answer the "
            f"pool IP {pool!r} for {hostname!r}; got {answer!r}"
        )

        deadline = time.time() + 30
        nft_out = _svc_ingress(devm)
        while (
            f"proto-dst {DIRECT_PORT}" in nft_out
            and time.time() < deadline
        ):
            time.sleep(1)
            nft_out = _svc_ingress(devm)
        assert f"proto-dst {DIRECT_PORT}" not in nft_out, (
            f"svc_ingress still carries a rule for port {DIRECT_PORT} "
            f"after live direct-withdraw:\n{nft_out}"
        )

        # Give the flush-rebuild a moment to actually close the port,
        # then confirm the Mac can no longer reach it (not just that
        # the rule text is gone).
        time.sleep(2)
        assert not _tcp_connect(pool, DIRECT_PORT, timeout=3), (
            f"Mac could still reach {pool}:{DIRECT_PORT} after live "
            f"direct-withdraw — svc_ingress accept should have been "
            f"withdrawn, restoring the forward hook's default drop"
        )

        assert _tart_pid(workspace.vm_name) == pid_before, (
            "VM was bounced by a live direct-withdraw reconcile; it "
            "must apply live only"
        )
    finally:
        subprocess.run(
            [devm.path, "exec", "docker", "rm", "-f", "e2e-direct-live-nc"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
