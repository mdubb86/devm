"""138: ssh via softnet survives the actual `devm install`/upgrade path.

Companion to test_133 (iron-proxy adoption) — same install cycle, but
tests the adjacent softnet path: after `devm install` (daemon reload +
adoption of the running tart/softnet processes), does raw
`ssh devm-<proj>` at the project's ProjectIP:22 STILL work?

Real-world failure (2026-08-03, everstone + buzztrack after v0.10.0
upgrade): daemon reload left softnet adopted but its port-forwarding
state stale. Guest sshd was healthy; `devm exec` still worked (uses
daemon-internal SSH tunnel). But raw `ssh devm-<proj>` at 127.42.0.x:22
failed instantly with `kex_exchange_identification: read: Connection
reset by peer` — softnet accepted the TCP but couldn't forward it
through gvisor to the guest sshd. Fix that recovered service was
`devm stop -y && devm start` (fresh softnet). This test would have
caught the regression before shipping.

Sequence:
  1. Cold-start.
  2. Assert `ssh -F <ssh_config> devm-<vm_name> true` succeeds (baseline).
  3. `devm install` — bootout daemon, reinstall, bootstrap. Same
     command `devm upgrade` runs after the binary swap.
  4. Short settle so daemon's adoption of the running VM + softnet
     child completes.
  5. Assert `ssh …` STILL succeeds — the crux.
  6. Teardown.

Deliberately does NOT assert softnet PID unchanged: whether softnet is
adopted-in-place or respawned is an implementation detail; what matters
user-side is that SSH keeps working across the install cycle.

Note on Touch ID prompts: this test's `devm install` adds a prompt on
top of the harness-primed sudo credential. Same tradeoff as test_133 —
the whole point is to exercise the install path with a live project.

sshd is unchanged; this test pins interactive ssh access, which is
orthogonal to the mutagen transport swap.
"""
from __future__ import annotations

import subprocess
import time
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


def _get_runtime_dir() -> Path:
    """The bootstrapped devm-e2e daemon's runtime directory. Mirrors
    test_93_ssh_access.py's helper of the same name."""
    return Path.home() / "Library" / "Application Support" / "devm-e2e"


def _ssh_ok(ssh_config: Path, vm_name: str) -> subprocess.CompletedProcess:
    """Run `ssh -F <ssh_config> devm-<vm_name> true` with a short
    ConnectTimeout so a broken softnet forward surfaces fast rather
    than blocking the test's own timeout."""
    return subprocess.run(
        ["ssh",
         "-F", str(ssh_config),
         "-o", "BatchMode=yes",
         "-o", "ConnectTimeout=10",
         f"devm-{vm_name}", "true"],
        capture_output=True, timeout=30,
    )


@pytest.mark.timeout(900)
@pytest.mark.slow
def test_ssh_via_softnet_survives_devm_install(devm, workspace, sandbox_name, devm_installed):
    runtime_dir = _get_runtime_dir()
    ssh_config = runtime_dir / "ssh_config"

    workspace.write_devmyaml()

    try:
        # 1. Cold-start.
        r = subprocess.run(
            [devm.path, "start"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        # ssh_config emit races the shell exit slightly — short settle.
        deadline = time.monotonic() + 10
        while time.monotonic() < deadline:
            if ssh_config.is_file() and \
               f"Host devm-{workspace.vm_name}" in ssh_config.read_text():
                break
            time.sleep(0.5)

        # 2. Baseline: SSH works pre-install. If this fails, the test's
        # premise is invalid — not the bug this test targets.
        r = _ssh_ok(ssh_config, workspace.vm_name)
        assert r.returncode == 0, (
            f"baseline ssh failed BEFORE install (not the target regression):\n"
            f"stderr={r.stderr.decode(errors='replace')!r}"
        )

        # 3. `devm install` — full lifecycle: bootout daemon, reinstall,
        # bootstrap. Timeout matches test_133.
        r = subprocess.run(
            [devm.path, "install"],
            capture_output=True, timeout=780, check=False,
        )
        assert r.returncode == 0, (
            f"devm install failed:\n"
            f"stdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )

        # 4. Settle: daemon needs to complete adoption of the running
        # tart process + its softnet child. Matches test_133's settle.
        time.sleep(2)

        # 5. The crux: softnet's SSH forward must still work. Failure
        # here reproduces the everstone/buzztrack breakage — softnet
        # adoption didn't preserve port-forwarding state.
        r = _ssh_ok(ssh_config, workspace.vm_name)
        assert r.returncode == 0, (
            f"ssh via softnet BROKE after `devm install` — this is the exact "
            f"failure mode observed on everstone/buzztrack after v0.10.0.\n"
            f"stderr={r.stderr.decode(errors='replace')!r}\n"
            f"Investigate: does softnet get adopted-in-place or respawned "
            f"by the new daemon? Does the new daemon's port-forwarding "
            f"table for the adopted VM include :22?"
        )
    finally:
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
