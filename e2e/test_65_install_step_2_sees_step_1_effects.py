"""65+66: install:/startup: internal ordering, plus the install->startup
boundary, in one boot.

Merges two independent-subsystem ordering probes that each cold-started
their own VM:
  - test_65: install: runs sequentially with completion ordering — step
    N+1 starts only after step N has FINISHED (the Go provisioner's
    sequencing).
  - test_66: a systemd service with `after: [dep]` starts only after
    `dep` has completed (the `after:` field rendering to a systemd
    After= dependency).

These are genuinely independent subsystems (provisioner sequencing vs.
systemd unit ordering), but both run during the same cold-start, so one
devm.yaml declaring both an ordered install: list and an ordered
services: pair — plus a third service that depends only on an install:
side effect — proves all three ordering invariants in a single boot.

The third service (`install_boundary`) closes a gap neither original
test covered: nothing pinned that ALL install: steps finish before
ANY startup: service runs (only that install:'s own steps are ordered,
and that step2/dep ordering *within* services holds). If install: and
startup: ever started concurrently, `install_boundary`'s check would
race the install: steps and could fail.

Setup:
  - install step 1: touch /tmp/from-step-1
  - install step 2: test -f /tmp/from-step-1 (fails if step 1 didn't
    finish first — install-internal ordering)
  - service "step1": touch /tmp/s1-ran; restart: no
  - service "step2": test -f /tmp/s1-ran && touch /tmp/s2-saw-s1;
    restart: no; after: [step1] (systemd After= ordering)
  - service "install_boundary": test -f /tmp/from-step-1 && touch
    /tmp/startup-saw-install; restart: no (install: -> startup: boundary
    ordering — no `after:` on install:, since install: isn't a service;
    the boundary is enforced by cold-start sequencing itself)

Devm dependencies:
  - install: is ordered + fail-fast; users may write step N+1 to depend
    on step N's side effects. If devm ever parallelizes install, the
    install-internal check fires.
  - The `after:` field in devm.yaml must render to a systemd After=
    dependency so users can express ordered startup.
  - ALL install: steps must complete before ANY startup: service starts.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_install_and_startup_ordering(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        install=[
            "touch /tmp/from-step-1",
            # If step 2 starts before step 1 finishes, this fails and
            # devm shell exits non-zero (loud failure per test_51).
            "test -f /tmp/from-step-1",
        ],
        services={
            "step1": {
                "exec": ["sh", "-c", "touch /tmp/s1-ran"],
                "restart": "no",
            },
            "step2": {
                "exec": ["sh", "-c",
                         "test -f /tmp/s1-ran && touch /tmp/s2-saw-s1"],
                "restart": "no",
                "after": ["step1"],
            },
            "install_boundary": {
                "exec": ["sh", "-c",
                         "test -f /tmp/from-step-1 && touch /tmp/startup-saw-install"],
                "restart": "no",
            },
        },
    )

    # First devm shell IS the cold-start; rc=0 means install steps ran in
    # order (a failing install: step exits devm shell non-zero).
    p = subprocess.run(
        [devm.path, "shell", "--", "true"],
        capture_output=True, cwd=str(workspace.path), timeout=180,
    )
    assert p.returncode == 0, (
        f"cold-start with sequential install steps failed; ordering invariant broken.\n"
        f"stdout={p.stdout.decode()!r}\nstderr={p.stderr.decode()!r}"
    )

    sandbox = TartSandbox(name=sandbox_name)
    current = sandbox.state()
    assert current == "running", (
        f"expected VM running after cold-start; got {current!r}"
    )

    # ---- install: internal ordering (65). ----
    # Belt-and-suspenders: confirm the marker is still on disk in the VM.
    r = sandbox.exec_shell("test -f /tmp/from-step-1 && echo present")
    assert r.ok and "present" in r.stdout, (
        f"/tmp/from-step-1 missing post-bringup; install ordering or "
        f"completion semantics changed. stdout={r.stdout!r} stderr={r.stderr!r}"
    )

    # ---- startup: internal ordering via systemd After= (66). ----
    r = sandbox.exec_shell("test -f /tmp/s1-ran && echo present")
    assert r.ok and "present" in r.stdout, (
        f"/tmp/s1-ran missing — step1 service didn't run or write marker. "
        f"stdout={r.stdout!r} stderr={r.stderr!r}"
    )
    r = sandbox.exec_shell("test -f /tmp/s2-saw-s1 && echo present")
    assert r.ok and "present" in r.stdout, (
        f"/tmp/s2-saw-s1 absent — step2 either didn't run or ran before "
        f"step1 completed. The after: field may not be rendering to a "
        f"systemd After= dependency. stdout={r.stdout!r} stderr={r.stderr!r}"
    )

    # ---- install: -> startup: boundary ordering (new). ----
    r = sandbox.exec_shell("test -f /tmp/startup-saw-install && echo present")
    assert r.ok and "present" in r.stdout, (
        f"/tmp/startup-saw-install absent — the install_boundary service ran "
        f"before install: finished (or didn't see its effects), meaning "
        f"startup: services are not reliably gated on ALL install: steps "
        f"completing. stdout={r.stdout!r} stderr={r.stderr!r}"
    )
