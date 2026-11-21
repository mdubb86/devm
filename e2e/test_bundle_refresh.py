"""End-to-end for the guest bundle refresh feature.

Single VM, sequence:
  1. Fresh cold-start: StateSnapshot.BundleFingerprint stamped to
     current daemon Fingerprint.
  2. devm reconcile with no cfg diff: NOT a refresh (control).
  3. Mutate the on-disk snapshot to a synthetic old fingerprint;
     devm reconcile: refreshes; output includes bundle refresh line;
     snapshot fingerprint updated to current.
  4. Mutate stale again; gdevm upgrade: refreshes; summary body
     printed; snapshot updated.
  5. Mutate stale a third time; kickstart the daemon; wait for
     health true; snapshot updated without user action
     (daemon-startup catchup).
"""
import json
import os
import subprocess
import tempfile
import time
from pathlib import Path

import pytest

from helpers.workspace import Workspace
from helpers.devm import Devm

# `install` marker gates this test to `just e2e-install` because step 5
# calls `sudo -n launchctl kickstart`; conftest.py's auto-detector only
# matches `devm install|uninstall|service restart` on source grep, so an
# explicit marker is required here.
pytestmark = [pytest.mark.devm, pytest.mark.install]


def _snapshot_path(runtime_dir: Path, project: str) -> Path:
    return runtime_dir / "state" / f"{project}.json"


def _read_snapshot(path: Path) -> dict:
    return json.loads(path.read_text())


def _write_snapshot(path: Path, data: dict) -> None:
    """Atomic write to match internal/serviceapi/state.go's WriteStateSnapshot
    pattern — the daemon watchdog reads this file every 60s from a background
    goroutine, so torn writes would surface as a load failure."""
    fd, tmp = tempfile.mkstemp(prefix=path.name + ".", dir=str(path.parent))
    try:
        with os.fdopen(fd, "w") as f:
            f.write(json.dumps(data))
        os.replace(tmp, path)
    except Exception:
        try:
            os.unlink(tmp)
        except OSError:
            pass
        raise


def _current_daemon_fingerprint(devm_path: str, cwd: str) -> str:
    r = subprocess.run(
        [devm_path, "status", "--json"],
        cwd=cwd, capture_output=True, timeout=10,
    )
    assert r.returncode == 0
    return json.loads(r.stdout)["daemon"]["fingerprint"]


def test_bundle_refresh_via_reconcile_gdevm_upgrade_and_startup(
    workspace: Workspace, devm: Devm
) -> None:
    workspace.write_devmyaml()
    devm.approve()
    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=180,
    )
    assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"

    project = workspace.vm_name
    runtime_dir = Path(os.path.expanduser("~/Library/Application Support/devm-e2e"))
    snap = _snapshot_path(runtime_dir, project)
    current = _current_daemon_fingerprint(devm.path, str(workspace.path))

    # 1. Post-cold-start seed.
    stored = _read_snapshot(snap)
    assert stored.get("bundle_fingerprint") == current, (
        "cold-start must seed BundleFingerprint to current daemon Fingerprint"
    )

    # 2. Reconcile with no cfg diff -> no refresh. Output should
    #    say "no changes".
    r = subprocess.run(
        [devm.path, "reconcile", "--yes"],
        cwd=str(workspace.path), capture_output=True, timeout=60,
    )
    assert r.returncode == 0
    assert b"no changes" in r.stdout + r.stderr
    stored = _read_snapshot(snap)
    assert stored["bundle_fingerprint"] == current, "reconcile with no diff must not touch fingerprint"

    # 3. Force stale, run reconcile.
    stored["bundle_fingerprint"] = "synthetic-old-fp"
    _write_snapshot(snap, stored)
    r = subprocess.run(
        [devm.path, "reconcile", "--yes"],
        cwd=str(workspace.path), capture_output=True, timeout=120,
    )
    assert r.returncode == 0, f"reconcile: {r.stderr.decode()}"
    combined = (r.stdout + r.stderr).decode()
    assert "bundle: refreshed" in combined, (
        f"reconcile output must include bundle refresh line:\n{combined}"
    )
    stored = _read_snapshot(snap)
    assert stored["bundle_fingerprint"] == current

    # 4. Force stale, run gdevm upgrade from inside the guest.
    stored["bundle_fingerprint"] = "synthetic-old-fp-2"
    _write_snapshot(snap, stored)
    r = subprocess.run(
        [devm.path, "exec", "gdevm", "upgrade"],
        cwd=str(workspace.path), capture_output=True, timeout=60,
    )
    assert r.returncode == 0, f"gdevm upgrade: {r.stderr.decode()}"
    combined = (r.stdout + r.stderr).decode()
    assert "bundle refreshed" in combined
    stored = _read_snapshot(snap)
    assert stored["bundle_fingerprint"] == current

    # 5. Force stale, kickstart daemon, verify auto-refresh at startup.
    stored["bundle_fingerprint"] = "synthetic-old-fp-3"
    _write_snapshot(snap, stored)
    subprocess.run(
        ["sudo", "-n", "launchctl", "kickstart", "-k", "system/com.devm.e2e.service"],
        check=True, timeout=15,
    )
    # Poll the snapshot fingerprint directly rather than status --json:
    # `kickstart -k` is asynchronous, and status probes can succeed against
    # the OLD daemon before it fully dies, leading to a false-early break.
    # The catchup sweep (internal/serviceapi/runner.go:404) is the sole
    # writer of this file during daemon startup, so watching for the flip
    # is the only signal that survives the async restart.
    deadline = time.time() + 90
    last_seen = None
    while time.time() < deadline:
        try:
            last_seen = _read_snapshot(snap).get("bundle_fingerprint")
        except (FileNotFoundError, json.JSONDecodeError):
            last_seen = None
        if last_seen == current:
            break
        time.sleep(1)
    else:
        raise AssertionError(
            f"daemon-startup catchup did not restore fingerprint within 90s "
            f"(last seen: {last_seen!r}, expected: {current!r})"
        )
    stored = _read_snapshot(snap)
    assert stored["bundle_fingerprint"] == current, (
        "daemon-startup catchup must refresh the snapshot without user action"
    )
