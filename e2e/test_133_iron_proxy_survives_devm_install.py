"""133: iron-proxy survives the actual `devm install`/upgrade path.

test_44 exercises `devm service restart` — `launchctl bootout + bootstrap`
of the SAME daemon binary. `devm install` uses the same bootout+bootstrap
but with a binary REPLACEMENT between them, plus install-specific setup
(plist re-write, helper reinstall, resolver file, etc.). The two paths
diverge in ways setsid may or may not cover.

Buzztrack (2026-07-31): user upgraded v0.9.12 → v0.9.13. Iron-proxy died
silently between the two daemon lifetimes — no shutdown log entry, no
graceful signal handler ran. That's the failure this test would catch:
if iron-proxy's PID is different after `devm install` completes (or it's
missing entirely), setsid didn't protect it and adoption failed.

Sequence:
  1. Cold-start with a minimal but non-trivial iron-proxy config
     (needs a real allowlist so iron-proxy actually starts).
  2. Note iron-proxy PID (via ps -axo command matching the project
     config path — same pattern as test_44).
  3. `devm install` — no-op reinstall over the currently-bootstrapped
     e2e daemon. Bootout, install, bootstrap. Full cycle.
  4. Wait for adoption (2s settle, same as test_44).
  5. Assert iron-proxy PID unchanged.
  6. Teardown.

Distinct from test_44 (service restart) and test_zz_install_uninstall_lifecycle
(install lifecycle without a live project). This is specifically:
'does iron-proxy for a running project survive the install cycle?'.

Note on Touch ID prompts: test_zz_install_uninstall_lifecycle batches
install/uninstall to minimize Touch ID prompts per suite run. This
test's `devm install` adds an extra prompt. That's intentional — the
whole point of this test is to exercise the install-cycle behavior for
a project with a live iron-proxy, which test_zz's install-only-in-
isolation cannot do. Do not consolidate this test into test_zz.
"""
from __future__ import annotations

import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


def _iron_proxy_pid_for(project_id: str) -> int | None:
    """Return the iron-proxy CHILD PID (not the setsid shim's PID) for
    this project, or None if not running.

    Differs from test_44's helper: test_44 returns the first match (which
    happens to be the shim, since ps orders by PID and shim is spawned
    first). We explicitly want the child here because it's the process
    that enforces egress — asserting on the shim's PID would still pass
    even if iron-proxy itself died and got respawned by the shim's Wait
    loop (which shouldn't happen with setsid detachment, but making the
    assertion robust to that case matters for THIS test).
    """
    r = subprocess.run(
        ["ps", "-axo", "pid=,command="],
        capture_output=True, text=True, check=True,
    )
    needle = f"/iron-proxy/{project_id}.yaml"
    for line in r.stdout.splitlines():
        if needle in line and "devm-setsid-shim" not in line:
            # Skip the shim entry; we want the iron-proxy PID directly.
            return int(line.strip().split(None, 1)[0])
    return None


@pytest.mark.timeout(900)
@pytest.mark.slow
def test_iron_proxy_survives_devm_install(devm, workspace, sandbox_name, devm_installed):
    workspace.write_devmyaml(
        install=["true"],
        services={"sleep": {"exec": ["/bin/sleep", "infinity"], "restart": "always"}},
        network={"allow": ["httpbin.org"]},
    )

    try:
        # 1. Cold-start.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # 2. Capture iron-proxy PID.
        pid_before = _iron_proxy_pid_for(workspace.slug)
        assert pid_before is not None, (
            f"iron-proxy should be running for {workspace.slug!r} after cold-start"
        )

        # 3. `devm install` — the actual upgrade path. Same command
        # `devm upgrade` runs. Timeout matches test_zz_install_uninstall_lifecycle.
        r = subprocess.run(
            [devm.path, "install"],
            capture_output=True, timeout=780, check=False,
        )
        assert r.returncode == 0, (
            f"devm install failed:\n"
            f"stdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )

        # 4. Settle window — matches test_44's 2s (daemon needs to run
        # DiscoverIronProxies + Adopt).
        time.sleep(2)

        # 5. Iron-proxy PID must be unchanged. If it isn't, setsid failed
        # to protect iron-proxy across the install cycle — that's the
        # buzztrack Bug B.
        pid_after = _iron_proxy_pid_for(workspace.slug)
        # Split the assertion so a CI failure points at the right diagnosis.
        if pid_after is None:
            raise AssertionError(
                f"iron-proxy is GONE after `devm install`: pid_before={pid_before}, "
                f"pid_after=None. Setsid didn't protect iron-proxy across the install "
                f"cycle — investigate what signal reached iron-proxy despite the setsid "
                f"shim (bootout kill semantics, cascade-kill on the daemon's session)."
            )
        if pid_after != pid_before:
            raise AssertionError(
                f"iron-proxy RESPAWNED across `devm install`: pid_before={pid_before}, "
                f"pid_after={pid_after}. Original process died (setsid protection issue) "
                f"AND a new one was spawned — either pexec backoff auto-restarted it, or "
                f"reconcile fired apply-iron-proxy during install. Adoption to preserve "
                f"the original PID clearly failed. Investigate DiscoverIronProxies + "
                f"AdoptIronProxies for why the surviving process wasn't picked up."
            )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
