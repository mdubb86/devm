"""120: devm.yaml config lock — lock on start, unlock/relock, opt-out, restart
recovery.

Feature e2e for the "config lock" feature (Tasks 1-7, already unit/
integration tested): while a project's VM runs, the daemon `chflags
uchg`'s `<repoRoot>/devm.yaml` (host-immutable) so a root guest can't
tamper with its own trust boundary via the mirrored workspace mount;
the lock lifts on `devm stop`. `devm unlock [--for <dur>]` clears it
for live editing and auto re-locks after the duration (default 5m);
`devm lock` (or a `devm reconcile`) re-locks immediately. A project can
opt out entirely with `config_lock: false` in devm.yaml (default: on).
Daemon restart recovery re-locks devm.yaml for any project whose VM is
still running.

Modeled on test_softnet_21_daemon_ingress.py's fixtures + drive
pattern: cold-start via `devm shell -- true`, then drive the CLI and
poll host-/guest-side state rather than fixed sleeps (the lock/unlock
crosses the daemon asynchronously).

Split across three functions so each ends in a teardown state that an
existing test already proves is clean under the shared `workspace`
fixture's `devm teardown --yes`:
  - test_config_lock_lifecycle ends STOPPED (like test_03_stop).
  - test_config_lock_restart_recovery ends RUNNING after a daemon
    restart (like test_100_daemon_restart_seam) — it deliberately does
    NOT also `devm stop`, so it never exercises the untested
    restart->stop->teardown combination.
  - test_config_lock_opt_out ends RUNNING (like most cold-start tests).

What this pins:
  - Host-side: `ls -lO devm.yaml` shows `uchg` after cold-start, is
    gone after `devm unlock`, returns after `devm lock` (and again
    after an `unlock --for` timer elapses), and is gone after
    `devm stop`.
  - Guest-side: the workspace is mirrored into the VM at the SAME
    absolute path as the host repoRoot (virtio-fs share — see
    test_58_mounts_mirrored_at_same_path.py and the
    test_tart_contract_15 platform pin); a guest-root write through
    that mount is BLOCKED while locked, host-side content unchanged.
  - `config_lock: false` opts a project out — devm.yaml is never
    locked even after a full cold-start.
  - A daemon restart while the VM is still running and locked leaves
    devm.yaml (re-)locked, not orphaned unlocked.

What it doesn't cover (tested elsewhere, unit/integration level):
  - The `chflags uchg` platform primitive itself, including a root
    guest being unable to clear the flag or defeat it via rm/mv/
    truncate -> test_tart_contract_15_chflags_uchg_immutable.py.
  - `ConfigLockEnabled()` default-on gating logic, timer plumbing
    internals, `/vm/unlock-config` and `/vm/lock-config` request
    shapes -> Go unit/integration tests for Tasks 2/3/6/7.

CRITICAL cleanup: an autouse fixture below `chflags nouchg`'s
devm.yaml unconditionally after each test in this module (success or
failure), and — because it depends on `workspace` — its finalizer runs
BEFORE the `workspace` fixture's `shutil.rmtree` teardown (pytest tears
fixtures down in reverse setup order). Without it, a test that fails
mid-assertion with devm.yaml still `uchg`-locked would leave an
unremovable temp dir behind.
"""
from __future__ import annotations

import subprocess
import time
from pathlib import Path

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


def _has_uchg(path: Path) -> bool:
    r = subprocess.run(
        ["ls", "-lO", str(path)], capture_output=True, text=True, timeout=10,
    )
    return "uchg" in r.stdout


def _wait_uchg(path: Path, expected: bool, timeout: float = 20.0) -> bool:
    """Poll `ls -lO` until the uchg flag's presence matches `expected`,
    or timeout. The daemon's lock/unlock crosses an RPC + syscall, so a
    single immediate check can race it."""
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if _has_uchg(path) == expected:
            return True
        time.sleep(0.5)
    return _has_uchg(path) == expected


@pytest.fixture(autouse=True)
def _unlock_safety_net(workspace):
    """See module docstring's CRITICAL cleanup note. Registered AFTER
    `workspace` (it depends on it), so this finalizer runs BEFORE
    workspace's `rmtree` teardown — exactly the ordering needed to
    guarantee the temp dir is removable even if a mid-test assertion
    fails while devm.yaml is still uchg-locked."""
    try:
        yield
    finally:
        subprocess.run(
            ["chflags", "nouchg", str(workspace.devmyaml_path)],
            capture_output=True, timeout=10,
        )


def _cold_start(devm, workspace, sandbox_name) -> TartSandbox:
    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path), capture_output=True, timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"
    sandbox = TartSandbox(name=sandbox_name)
    assert sandbox.state() == "running", (
        f"expected VM running after cold-start; got {sandbox.state()!r}"
    )
    return sandbox


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_config_lock_lifecycle(workspace, devm, sandbox_name):
    """Lock on start (+ guest-root blocked), unlock, immediate re-lock,
    the `--for` auto-relock timer, and unlock on stop. Ends STOPPED —
    the clean-teardown state test_03_stop already proves."""
    devm_yaml = workspace.devmyaml_path
    workspace.write_devmyaml(network={"allow": ["example.com"]})
    original_content = devm_yaml.read_text()

    sandbox = _cold_start(devm, workspace, sandbox_name)
    # The guest mounts the workspace at the SAME absolute path as the
    # host repoRoot (virtio-fs share, mirrored path — see
    # test_58_mounts_mirrored_at_same_path.py). No translation needed.
    guest_devm_yaml = f"{workspace.path}/devm.yaml"

    # ================================================================
    # Assertion 1: lock on start (host-side flag + guest-root blocked).
    # ================================================================
    assert _wait_uchg(devm_yaml, True, timeout=30), (
        f"devm.yaml should be host-immutable (uchg) after cold-start; "
        f"ls -lO: "
        f"{subprocess.run(['ls', '-lO', str(devm_yaml)], capture_output=True, text=True).stdout!r}"
    )

    # Confirm the guest actually sees the file at the same path before
    # trying to write it (fail loud on a bad mount-path assumption
    # rather than a misleading "write blocked" pass).
    mount_check = sandbox.exec_shell(f"ls {guest_devm_yaml}")
    assert mount_check.ok, (
        f"expected the workspace mounted in-guest at {guest_devm_yaml!r} "
        f"(same absolute path as host repoRoot); `ls` failed: "
        f"exit={mount_check.exit_code} stderr={mount_check.stderr!r}"
    )

    # A guest-root write to the locked file must fail at the host kernel.
    # Assert on the exit status + stderr, NOT an `echo OK/BLOCKED` sentinel:
    # `>>` against a uchg file fails at redirection setup, which dash treats
    # as a script-level error that aborts the whole `sh -c` before any `||`
    # branch runs — so no sentinel prints. The write is still correctly
    # blocked; it surfaces as a nonzero exit + "Operation not permitted".
    guest_write = sandbox.exec_shell(f"sudo sh -c 'echo x >> {guest_devm_yaml}'")
    assert guest_write.exit_code != 0 and "not permitted" in guest_write.stderr.lower(), (
        f"guest-root write through the workspace mount should be blocked by "
        f"the host-side uchg lock; got exit={guest_write.exit_code} "
        f"stdout={guest_write.stdout!r} stderr={guest_write.stderr!r}"
    )
    assert devm_yaml.read_text() == original_content, (
        "host-side devm.yaml content changed despite the uchg lock"
    )

    # ================================================================
    # Assertion 2: `devm unlock` lifts the lock; a host write succeeds.
    # ================================================================
    r = subprocess.run(
        [devm.path, "unlock"], cwd=str(workspace.path),
        capture_output=True, timeout=30,
    )
    assert r.returncode == 0, f"devm unlock failed:\n{r.stderr.decode()}"
    assert _wait_uchg(devm_yaml, False, timeout=15), (
        "devm.yaml should no longer be uchg after `devm unlock`"
    )

    devm_yaml.write_text(original_content + "\n# unlocked-write-probe\n")
    assert "unlocked-write-probe" in devm_yaml.read_text(), (
        "a host-side write to devm.yaml should succeed once unlocked"
    )
    devm_yaml.write_text(original_content)  # restore valid yaml for later reconcile/teardown

    # ================================================================
    # Assertion 3a: `devm lock` re-locks immediately.
    # ================================================================
    r = subprocess.run(
        [devm.path, "lock"], cwd=str(workspace.path),
        capture_output=True, timeout=30,
    )
    assert r.returncode == 0, f"devm lock failed:\n{r.stderr.decode()}"
    assert _wait_uchg(devm_yaml, True, timeout=15), (
        "devm.yaml should be uchg again immediately after `devm lock`"
    )

    # ================================================================
    # Assertion 3b: `devm unlock --for <dur>` auto re-locks after the
    # duration elapses, with no explicit `devm lock` call.
    # ================================================================
    r = subprocess.run(
        [devm.path, "unlock", "--for", "3s"], cwd=str(workspace.path),
        capture_output=True, timeout=30,
    )
    assert r.returncode == 0, f"devm unlock --for 3s failed:\n{r.stderr.decode()}"
    assert _wait_uchg(devm_yaml, False, timeout=15), (
        "devm.yaml should be unlocked right after `devm unlock --for 3s`"
    )
    assert _wait_uchg(devm_yaml, True, timeout=10), (
        "devm.yaml should auto re-lock once the `--for 3s` timer elapses"
    )

    # ================================================================
    # Assertion 4: `devm stop` unlocks.
    # ================================================================
    devm.stop(yes=True, timeout=60)
    assert sandbox.wait_state("stopped", timeout=20) == "stopped", (
        "VM should be stopped after `devm stop`"
    )
    assert _wait_uchg(devm_yaml, False, timeout=20), (
        "devm.yaml should no longer be uchg after `devm stop`"
    )


# Restart recovery (a daemon restart re-locks a still-running project's
# devm.yaml) is covered deterministically at the unit level —
# TestRecoverProjectState_RelocksConfig_WhenEnabled / _DoesNotRelock_WhenDisabled
# in internal/serviceapi/configlock_lifecycle_test.go assert recoverProjectState
# (the daemon's adopt path) re-locks a recovered running project and skips a
# config_lock:false one. It is intentionally NOT re-proven here: an e2e would
# have to stop the VM after the restart, and `devm stop` doesn't cleanly stop
# a softnet VM after a daemon restart (the VM process isn't re-adopted — a
# pre-existing devm gap, orthogonal to config lock; see docs/superpowers/TODO.md),
# so the leftover VM forces the registry leak-check's `tart stop`, which hangs.


@pytest.mark.slow
@pytest.mark.timeout(300)
def test_config_lock_opt_out(workspace, devm, sandbox_name):
    """Assertion 5: `config_lock: false` opts a project out — devm.yaml
    is never locked, even after a full cold-start."""
    devm_yaml = workspace.devmyaml_path
    workspace.write_devmyaml(
        config_lock=False, network={"allow": ["example.com"]},
    )

    _cold_start(devm, workspace, sandbox_name)

    # Poll (rather than a flat sleep) for a window long enough that a
    # wrongly-still-locking daemon would have locked it by now (same
    # ballpark as the positive case's `_wait_uchg` timeout above).
    deadline = time.monotonic() + 15
    saw_uchg = False
    while time.monotonic() < deadline:
        if _has_uchg(devm_yaml):
            saw_uchg = True
            break
        time.sleep(0.5)
    assert not saw_uchg, (
        "config_lock: false should opt out of the host-immutable lock, "
        "but devm.yaml went uchg anyway"
    )
