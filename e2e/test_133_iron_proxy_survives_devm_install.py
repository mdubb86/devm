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
"""
from __future__ import annotations

import subprocess
import time

import pytest

pytestmark = pytest.mark.devm


def _iron_proxy_pid_for(project_id: str) -> int | None:
    """Return the PID of the iron-proxy process for this project, or None.

    Same pattern as test_44 — match on the config path in the command
    (unambiguous per project).
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
        assert pid_after == pid_before, (
            f"iron-proxy PID changed across `devm install`: "
            f"before={pid_before} after={pid_after}.\n"
            f"Either setsid didn't protect iron-proxy across the install cycle,\n"
            f"or adoption failed to re-attach the surviving process."
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
