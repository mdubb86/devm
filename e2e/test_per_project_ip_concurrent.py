"""Two projects declaring a `direct: true` service on the SAME port
(:5432) run concurrently, each reachable only at its own allocated
per-project loopback IP (127.42.0.N) — the "impossible before, works
after" case for per-project bind isolation (B3). Before this feature,
two projects claiming the same host-facing port collided fail-loud
(portclaims.go); after it, each project owns a distinct 127.42.0.N and
ports never collide. See
docs/superpowers/specs/2026-07-19-per-project-bind-isolation-design.md.

Adapted from the task-7 brief's sketch (do NOT invent APIs that don't
exist — see .superpowers/sdd/task-7-report.md for the full list):
  - `helpers.workspace.Workspace` has no `write_devmyaml_dict`; this
    uses the real `write_devmyaml(**sections)` signature instead.
  - `Devm()`/`Workspace()` take positional constructor args per
    helpers/devm.py and helpers/workspace.py (path/cwd, path/slug/
    vm_name) — not zero-arg constructors. This test needs TWO
    independent projects, so it builds two Workspace/Devm pairs by
    hand (mirroring conftest.py's `workspace`/`devm` fixtures) rather
    than requesting the singular fixtures, which are scoped to one
    project per test.
  - `devm status --all --json` carries no `project_ip` field
    (internal/serviceapi/statusall.go's ProjectStatus only has
    name/vm_running/proxy) — this reads the daemon's persisted
    StateSnapshot file directly instead, the same on-disk source
    AdoptIronProxies itself reads back on daemon restart
    (internal/serviceapi/state.go's `ProjectIP` field,
    json:"project_ip").
  - Reachability is asserted by dialing each project's ProjectIP
    directly rather than resolving the `.test` hostname first, so the
    core assertion doesn't inherit the Mac-side-DNS-under-isolation gap
    already documented in test_110_direct_cold_start.py (the isolated
    e2e lane's DEVM_DNS_ADDR is ephemeral and exposes no queryable
    port). A soft, non-blocking DNS check is still included for the
    common real-install case, matching test_110's warn-and-continue
    pattern.
  - Reachability itself needs the 127.42.0.0/24 lo0 aliases actually
    provisioned (`devm install`'s portbinder LaunchDaemon) — this
    self-skips if 127.42.0.1 isn't bindable, same EADDRNOTAVAIL(49)
    signal test_loopback_contract.py pins.

Uses the proven `docker run busybox nc -l` scaffold from
test_110_direct_cold_start.py: a tiny detached container per project,
looping a project-specific marker on the declared port.
"""
from __future__ import annotations

import json
import os
import secrets
import shutil
import socket
import subprocess
import tempfile
from pathlib import Path

import pytest

from helpers import Devm, Workspace, registry
from helpers.direct import dig_a, dns_addr, tcp_read_banner, wait_reachable
from helpers.exec_retry import devm_exec_with_retry

pytestmark = pytest.mark.devm

DIRECT_PORT = 5432


def _runtime_dir() -> Path:
    if os.environ.get("E2E_ISOLATE") == "1":
        d = os.environ.get("DEVM_RUNTIME_DIR")
        if d:
            return Path(d)
    return Path.home() / "Library" / "Application Support" / "devm"


def _project_ip(project_id: str) -> str | None:
    """Read ProjectIP from the daemon's persisted StateSnapshot for
    project_id — the same on-disk field (json:"project_ip") that
    AdoptIronProxies reads back on daemon restart
    (internal/serviceapi/state.go). There is no CLI/HTTP-facing surface
    for this value today (see module docstring)."""
    p = _runtime_dir() / "state" / f"{project_id}.json"
    if not p.exists():
        return None
    try:
        return json.loads(p.read_text()).get("project_ip") or None
    except (OSError, ValueError):
        return None


def _pool_alias_available() -> bool:
    """True iff 127.42.0.1 is bindable, i.e. the portbinder helper's
    lo0-alias provisioning (`devm install`) has run since the last
    reboot. EADDRNOTAVAIL(49) means the alias is absent."""
    try:
        with socket.socket() as s:
            s.bind(("127.42.0.1", 0))
        return True
    except OSError as e:
        if e.errno == 49:
            return False
        raise


def _mk_project(devm_path: str, label: str) -> tuple[Workspace, Devm]:
    name = f"e2e-ipconc-{label}-{secrets.token_hex(3)}"
    path = Path(tempfile.mkdtemp(prefix=f"devm-e2e-{name}-")).resolve()
    registry.append("workspace", str(path))
    ws = Workspace(path, slug=name, vm_name=name)
    hostname = f"echo.{name}.test"
    ws.write_devmyaml(
        docker=True,
        services={
            "echo": {"port": DIRECT_PORT, "hostname": hostname, "direct": True},
        },
    )
    d = Devm(devm_path, cwd=str(path))
    return ws, d


def _teardown_project(ws: Workspace, d: Devm) -> None:
    subprocess.run(
        [d.path, "exec", "docker", "rm", "-f", "echo"],
        cwd=str(ws.path), capture_output=True, timeout=30,
    )
    subprocess.run(
        [d.path, "teardown", "--yes"],
        cwd=str(ws.path), capture_output=True, timeout=60,
    )
    shutil.rmtree(ws.path, ignore_errors=True)
    registry.remove("workspace", str(ws.path))


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_per_project_ip_concurrent_isolation(devm_path):
    if not _pool_alias_available():
        pytest.skip(
            "127.42.0.0/24 lo0 aliases not provisioned; run `devm install` "
            "first (see docs/superpowers/specs/"
            "2026-07-19-per-project-bind-isolation-design.md)"
        )

    a, devm_a = _mk_project(devm_path, "a")
    b, devm_b = _mk_project(devm_path, "b")
    try:
        for ws, d in ((a, devm_a), (b, devm_b)):
            r = subprocess.run(
                [d.path, "shell", "--", "true"],
                cwd=str(ws.path), capture_output=True, timeout=480,
            )
            assert r.returncode == 0, (
                f"cold-start for {ws.slug} failed:\nstderr={r.stderr.decode()!r}"
            )

        ip_a = _project_ip(a.slug)
        ip_b = _project_ip(b.slug)
        assert ip_a and ip_a.startswith("127.42.0."), f"bad ProjectIP for A: {ip_a!r}"
        assert ip_b and ip_b.startswith("127.42.0."), f"bad ProjectIP for B: {ip_b!r}"
        assert ip_a != ip_b, (
            f"A and B were allocated the SAME ProjectIP {ip_a!r} — pool "
            f"allocator broken"
        )

        for ws, d in ((a, devm_a), (b, devm_b)):
            marker = f"{ws.slug}-marker".encode()
            run = devm_exec_with_retry(
                d.path,
                ["docker", "run", "-d", "--name", "echo",
                 "-p", f"{DIRECT_PORT}:{DIRECT_PORT}", "busybox",
                 "sh", "-c",
                 f"while true; do printf '%s' '{marker.decode()}' | "
                 f"nc -l -p {DIRECT_PORT}; done"],
                cwd=str(ws.path), timeout=120,
            )
            assert run.returncode == 0, (
                f"docker run busybox nc failed for {ws.slug}: "
                f"{run.stderr.decode()!r}"
            )

        # ---- Core assertion: each project is reachable ONLY at its own
        # ---- allocated IP, on the SAME declared port — the "impossible
        # ---- before, works after" case this test exists to pin. ----
        for ws, ip in ((a, ip_a), (b, ip_b)):
            marker = f"{ws.slug}-marker".encode()
            assert wait_reachable(ip, DIRECT_PORT, timeout=60), (
                f"{ip}:{DIRECT_PORT} (project {ws.slug}) never became reachable"
            )
            got = tcp_read_banner(ip, DIRECT_PORT, marker, timeout=5)
            assert got == marker, (
                f"expected {marker!r} from {ip}:{DIRECT_PORT} ({ws.slug}), "
                f"got {got!r} — bind isolation broken (cross-project bleed "
                f"or stale listener)"
            )

        # ---- Soft bonus check: DNS answers each hostname with THAT
        # ---- project's own IP, never the other's. Ephemeral
        # ---- DEVM_DNS_ADDR under E2E_ISOLATE=1 exposes no queryable
        # ---- port (see test_110's KNOWN GAP) — warn-and-continue,
        # ---- since DNS correctness isn't this test's core subject
        # ---- (that's dns_test.go + test_110). ----
        dns_host, dns_port = dns_addr()
        if dns_port == 0:
            print(
                "WARNING: DEVM_DNS_ADDR is ephemeral (127.0.0.1:0) in the "
                "isolated e2e lane; skipping the Mac-side DNS sub-assertion "
                "only (see test_110_direct_cold_start.py's KNOWN GAP)."
            )
        else:
            for ws, ip in ((a, ip_a), (b, ip_b)):
                hostname = f"echo.{ws.slug}.test"
                answer = dig_a(hostname, dns_host, dns_port)
                assert answer == ip, (
                    f"DNS for {hostname!r} should answer {ip!r}; got {answer!r}"
                )
    finally:
        _teardown_project(a, devm_a)
        _teardown_project(b, devm_b)
