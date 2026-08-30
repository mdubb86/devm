"""220: the in-sync guard's four fast-path outcomes.

GuardCheck (internal/serviceapi/mutagen_guard.go), invoked from both
SetupPhase (cold-start) and apply_live's setupSingleSession (a live
`devm reconcile` adding a new `volumes:` entry), compares a candidate
entity's Mac mirror and guest target before letting mutagen touch
either side:

  - either side empty -> always OK (nothing to reconcile against).
  - both non-empty and aligned (same count/size/top-hash) -> OK.
  - both non-empty and divergent -> rejected, naming the label.

Each sub-scenario drives the guard through a live `devm reconcile`
that adds a brand-new `volumes:` entry to an already-running project,
having pre-populated the Mac mirror and/or guest target directly
(bypassing mutagen entirely) beforehand -- this is the realistic
"user already has content on both sides and wants to bring it under
mutagen" path apply_live's OpAdd guard exists for.
"""
# NOTE: mutagen setup runs pre-install:/startup: under the tart exec transport
from __future__ import annotations
import subprocess

import pytest

from helpers.mutagen_e2e import mirror_path, session_prefix, sync_list

pytestmark = pytest.mark.devm


def _start(devm, workspace):
    r = subprocess.run(
        [devm.path, "start"], cwd=str(workspace.path),
        capture_output=True, timeout=180,
    )
    assert r.returncode == 0, f"devm start failed:\n{r.stderr.decode()}"


def _write_guest_dir(devm, workspace, guest_path: str, files: dict[str, str]) -> None:
    """Create guest_path (as root) and populate it with files (name ->
    content) before it's ever declared as a `volumes:` entry."""
    script = f"mkdir -p {guest_path}\n"
    for name, content in files.items():
        script += f"printf '%s' {content!r} > {guest_path}/{name}\n"
    script += f"chown -R devm:devm {guest_path}\n"
    r = subprocess.run(
        [devm.path, "shell", "--", "sudo", "sh", "-c", script],
        cwd=str(workspace.path), capture_output=True, timeout=60,
    )
    assert r.returncode == 0, f"guest pre-population failed:\n{r.stderr.decode()}"


def _write_mac_mirror(vm_name: str, label: str, files: dict[str, str]) -> None:
    d = mirror_path(vm_name, label)
    d.mkdir(parents=True, exist_ok=True)
    for name, content in files.items():
        (d / name).write_text(content)


def _add_volume_and_reconcile(devm, workspace, label: str, guest_path: str):
    devm.unlock()
    workspace.patch_devmyaml(volumes={label: guest_path})
    return devm.reconcile(yes=True, timeout=60, check=False)


@pytest.mark.timeout(240)
def test_guard_both_empty_ok(devm, workspace):
    _start(devm, workspace)
    try:
        r = _add_volume_and_reconcile(devm, workspace, "emptyboth", "/mnt/emptyboth")
        assert r.returncode == 0, f"reconcile failed:\n{r.stderr.decode()}"

        sessions = sync_list(session_prefix(workspace.vm_name))
        names = [s["name"] for s in sessions]
        assert any(n.endswith("-emptyboth") for n in names), (
            f"expected a session for label 'emptyboth', got {names}"
        )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                        capture_output=True, timeout=60)


@pytest.mark.timeout(240)
def test_guard_mac_populated_guest_empty_ok(devm, workspace):
    _start(devm, workspace)
    try:
        _write_mac_mirror(workspace.vm_name, "macfirst", {"f.txt": "hello"})

        r = _add_volume_and_reconcile(devm, workspace, "macfirst", "/mnt/macfirst")
        assert r.returncode == 0, f"reconcile failed:\n{r.stderr.decode()}"

        sessions = sync_list(session_prefix(workspace.vm_name))
        names = [s["name"] for s in sessions]
        assert any(n.endswith("-macfirst") for n in names), (
            f"expected a session for label 'macfirst', got {names}"
        )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                        capture_output=True, timeout=60)


@pytest.mark.timeout(240)
def test_guard_both_aligned_ok(devm, workspace):
    _start(devm, workspace)
    try:
        _write_guest_dir(devm, workspace, "/mnt/aligned", {"f.txt": "hello"})
        _write_mac_mirror(workspace.vm_name, "aligned", {"f.txt": "hello"})

        r = _add_volume_and_reconcile(devm, workspace, "aligned", "/mnt/aligned")
        assert r.returncode == 0, (
            f"reconcile should accept two aligned sides:\n{r.stderr.decode()}"
        )

        sessions = sync_list(session_prefix(workspace.vm_name))
        names = [s["name"] for s in sessions]
        assert any(n.endswith("-aligned") for n in names), (
            f"expected a session for label 'aligned', got {names}"
        )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                        capture_output=True, timeout=60)


@pytest.mark.timeout(240)
def test_guard_both_divergent_rejected(devm, workspace):
    _start(devm, workspace)
    try:
        # Divergent entry counts: guest has one file, mac mirror has two.
        _write_guest_dir(devm, workspace, "/mnt/divergent", {"only.txt": "guest-only"})
        _write_mac_mirror(workspace.vm_name, "divergent", {"a.txt": "aaa", "b.txt": "bbb"})

        r = _add_volume_and_reconcile(devm, workspace, "divergent", "/mnt/divergent")
        assert r.returncode != 0, (
            f"reconcile should reject divergent sides; got exit 0:\n{r.stdout.decode()!r}"
        )
        err = r.stderr.decode()
        assert "in-sync guard failed for divergent" in err, (
            f"expected the guard's rejection to name the label 'divergent'; got:\n{err}"
        )

        sessions = sync_list(session_prefix(workspace.vm_name))
        names = [s["name"] for s in sessions]
        assert not any(n.endswith("-divergent") for n in names), (
            f"no session should have been created for the rejected label 'divergent', got {names}"
        )
    finally:
        subprocess.run([devm.path, "teardown", "--yes"], cwd=str(workspace.path),
                        capture_output=True, timeout=60)
