"""313: orphan-archive surfacing on reconcile and status.

When a snapshot archive remains on disk after its `snapshots:` entry
is removed from devm.yaml, `devm reconcile` must land successfully
(the orphan is advisory, not a refusal) and `devm status` must print a
prominent "Snapshot warnings:" block naming the orphan and its
remediation command. `devm snapshot clear <name>` then wipes the
archives and the warning disappears from the next `devm status`.

Flow:
  1. Cold-start with `snapshots.blob` declared stream-mode (no `path:`),
     backup_blob / restore_blob hooks in devm.sh, plus a trivial env
     marker so there's non-snapshot reconcile work on the next pass.
  2. `devm snapshot create blob` — captures a stream archive. Verify
     the archive landed on the Mac side.
  3. Rewrite devm.yaml dropping the `snapshots.blob` entry (keeping the
     env marker, with a new value so there's a live-bucket change the
     next reconcile has to apply) and strip the backup/restore hooks
     from devm.sh for symmetry.
  4. `devm reconcile` — expect exit 0. The archive dir on disk has no
     matching yaml entry; the snapshot subsystem emits an advisory
     warning instead of refusing, and the env-marker change (a live
     bucket) lands alongside.
  5. `devm status` — stdout contains "Snapshot warnings:", the orphan
     line naming `"blob"`, and the `devm snapshot clear blob`
     remediation command.
  6. `devm snapshot clear blob` — expect exit 0, stdout says
     `cleared N archive(s) for blob`.
  7. `devm status` — the warnings block is gone.

Pins:
  - Reconcile lands with warnings populated instead of refusing when
    an archive dir has no yaml entry.
  - `devm status` surfaces the orphan with its remediation command.
  - `devm snapshot clear <name>` resolves the orphan.
"""
from __future__ import annotations

import glob
import subprocess
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


_DEVM_SH_WITH_HOOKS = """#!/usr/bin/env bash
set -eo pipefail
backup_blob()  { echo HELLO; }
restore_blob() { cat > /dev/null; }
"""

_DEVM_SH_WITHOUT_HOOKS = """#!/usr/bin/env bash
set -eo pipefail
"""


def _snapshot_dir(workspace, entry: str) -> Path:
    """Mac-side archive directory for one snapshot entry under the
    devm-e2e identity's RuntimeDir."""
    runtime_dir = Path.home() / "Library" / "Application Support" / "devm-e2e"
    return runtime_dir / workspace.vm_name / "snapshots" / entry


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_snapshot_orphan_surfacing(workspace, devm, sandbox_name):
    # Step 1: stream-mode yaml with the blob entry + env marker.
    workspace.write_devmyaml(
        no_repo=True,
        snapshots={
            "blob": {},
        },
        env={"ORPHAN_MARKER": "initial"},
    )
    workspace.write_devm_sh(_DEVM_SH_WITH_HOOKS)

    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=600,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    # Step 2: capture a stream-mode archive and verify it landed.
    devm.snapshot("create", "blob", timeout=180)

    archive_dir = _snapshot_dir(workspace, "blob")
    matches = glob.glob(str(archive_dir / "*.zst"))
    assert len(matches) >= 1, (
        f"expected at least one .zst archive for blob under {archive_dir}, "
        f"got {matches}"
    )

    # Step 3: drop the blob entry from yaml (keeping the env marker, and
    # flipping its value so reconcile has an env live-bucket change to
    # apply). Strip the backup_blob/restore_blob hooks from devm.sh so
    # the devm.sh stays consistent with the yaml.
    workspace.write_devmyaml(
        no_repo=True,
        env={"ORPHAN_MARKER": "after_orphan"},
    )
    workspace.write_devm_sh(_DEVM_SH_WITHOUT_HOOKS)

    # The yaml + devm.sh edits trip the approve gate; reconcile refuses
    # until the change is approved.
    devm.approve()

    # Step 4: reconcile. Orphan surfacing is advisory — the snapshot
    # subsystem must not refuse the whole reconcile. Exit 0 proves the
    # env live-bucket change (and every other reconcile step) landed
    # alongside the warning.
    reconcile = devm.reconcile(yes=True, timeout=120, check=False)
    assert reconcile.returncode == 0, (
        f"reconcile refused instead of surfacing orphan:\n"
        f"stdout={reconcile.stdout.decode()!r}\n"
        f"stderr={reconcile.stderr.decode()!r}"
    )

    # Step 5: status prints the "Snapshot warnings:" block with the
    # orphan line and its remediation command.
    status = subprocess.run(
        [devm.path, "status"],
        cwd=str(workspace.path), capture_output=True, timeout=20,
    )
    status_stdout = status.stdout.decode()
    assert "Snapshot warnings:" in status_stdout, (
        f"status missing 'Snapshot warnings:' block:\n{status_stdout!r}"
    )
    assert '"blob"' in status_stdout, (
        f"status missing quoted entry name: {status_stdout!r}"
    )
    assert "devm snapshot clear blob" in status_stdout, (
        f"status missing clear-remediation: {status_stdout!r}"
    )

    # Step 6: clear the orphan. The command wipes the archive dir and
    # reports how many archive files it removed.
    clear = devm.snapshot("clear", "blob", timeout=60)
    clear_stdout = clear.stdout.decode()
    assert "cleared" in clear_stdout and "blob" in clear_stdout, (
        f"unexpected clear output: {clear_stdout!r}"
    )

    # Step 7: with the archive dir gone, DiscoverOrphans returns nothing
    # and the warnings block is no longer rendered.
    status_after = subprocess.run(
        [devm.path, "status"],
        cwd=str(workspace.path), capture_output=True, timeout=20,
    )
    status_after_stdout = status_after.stdout.decode()
    assert "Snapshot warnings:" not in status_after_stdout, (
        f"status still shows warnings block after clear:\n{status_after_stdout!r}"
    )
