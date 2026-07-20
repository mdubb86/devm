"""Start project A → note its allocated ProjectIP. Stop A. Start B.
Verify B was allocated A's now-freed IP — proves ReleaseProjectIP
(internal/serviceapi/projectip.go, called unconditionally from
`/vm/stop`) actually frees the slot rather than leaking it, and that
AllocateProjectIP's "lowest free" algorithm picks it back up.

Adapted from the task-7 brief's sketch (see
.superpowers/sdd/task-7-report.md for the full rationale):
  - `devm status --all --json` carries no `project_ip` field
    (internal/serviceapi/statusall.go's ProjectStatus only has
    name/vm_running/proxy — no per-project IP surface exists on the
    CLI/HTTP layer today). This reads the daemon's persisted
    StateSnapshot file directly instead: the same on-disk field
    (json:"project_ip") AdoptIronProxies reads back on daemon restart.
  - `Devm()`/`Workspace()` take positional constructor args per
    helpers/devm.py and helpers/workspace.py — this test needs TWO
    sequential projects, so it builds Workspace/Devm pairs by hand
    (mirroring conftest.py's fixtures) rather than the singular
    `workspace`/`devm` fixtures (scoped to one project per test).

No sudo, no portbinder helper required: AllocateProjectIP/
ReleaseProjectIP are unconditional in-memory + persisted-state
operations on every /vm/start and /vm/stop (see vm.go) — they don't
depend on the root port-binder helper actually being installed or
able to bind anything, so this test runs the same whether or not
`devm install` has provisioned the lo0 alias pool.
"""
from __future__ import annotations

import json
import os
import secrets
import shutil
import subprocess
import tempfile
from pathlib import Path

import pytest

from helpers import Devm, Workspace, registry

pytestmark = pytest.mark.devm


def _runtime_dir() -> Path:
    if os.environ.get("E2E_ISOLATE") == "1":
        d = os.environ.get("DEVM_RUNTIME_DIR")
        if d:
            return Path(d)
    return Path.home() / "Library" / "Application Support" / "devm"


def _project_ip(project_id: str) -> str | None:
    p = _runtime_dir() / "state" / f"{project_id}.json"
    if not p.exists():
        return None
    try:
        return json.loads(p.read_text()).get("project_ip") or None
    except (OSError, ValueError):
        return None


def _mk_project(devm_path: str, label: str) -> tuple[Workspace, Devm]:
    name = f"e2e-iprelease-{label}-{secrets.token_hex(3)}"
    path = Path(tempfile.mkdtemp(prefix=f"devm-e2e-{name}-")).resolve()
    registry.append("workspace", str(path))
    ws = Workspace(path, slug=name, vm_name=name)
    ws.write_devmyaml()
    d = Devm(devm_path, cwd=str(path))
    return ws, d


def _teardown_project(ws: Workspace, d: Devm) -> None:
    subprocess.run(
        [d.path, "teardown", "--yes"],
        cwd=str(ws.path), capture_output=True, timeout=60,
    )
    shutil.rmtree(ws.path, ignore_errors=True)
    registry.remove("workspace", str(ws.path))


@pytest.mark.timeout(600)
def test_release_then_realloc(devm_path):
    a, devm_a = _mk_project(devm_path, "a")
    b = None
    devm_b = None
    try:
        r = subprocess.run(
            [devm_a.path, "shell", "--", "true"],
            cwd=str(a.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start A failed:\n{r.stderr.decode()!r}"

        ip_a = _project_ip(a.slug)
        assert ip_a and ip_a.startswith("127.42.0."), f"unexpected ProjectIP for A: {ip_a!r}"

        r = subprocess.run(
            [devm_a.path, "stop", "--yes"],
            cwd=str(a.path), capture_output=True, timeout=60,
        )
        assert r.returncode == 0, f"stop A failed:\n{r.stderr.decode()!r}"

        # Confirm the slot is actually released before B starts — the
        # StateSnapshot's project_ip clears to empty on /vm/stop
        # (internal/serviceapi/state.go's ProjectIP doc comment).
        ip_a_after_stop = _project_ip(a.slug)
        assert not ip_a_after_stop, (
            f"expected A's ProjectIP to clear after stop; still {ip_a_after_stop!r}"
        )

        b, devm_b = _mk_project(devm_path, "b")
        r = subprocess.run(
            [devm_b.path, "shell", "--", "true"],
            cwd=str(b.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start B failed:\n{r.stderr.decode()!r}"

        ip_b = _project_ip(b.slug)
        assert ip_b == ip_a, (
            f"expected B to reuse A's released IP {ip_a!r}; got {ip_b!r} — "
            f"ReleaseProjectIP or the lowest-free allocator is broken "
            f"(note: flaky if another project concurrently claims this "
            f"slot on a shared, non-isolated daemon)"
        )
    finally:
        if b is not None and devm_b is not None:
            _teardown_project(b, devm_b)
        _teardown_project(a, devm_a)
