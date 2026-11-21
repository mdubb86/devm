"""312: `devm snapshot restore` refuses when the archive's captured
mode disagrees with what devm.yaml now declares, and `--force`
bypasses the refusal.

Flow:
  1. Cold-start with `snapshots.blob` declared stream-mode (no `path:`)
     and backup_blob / restore_blob in devm.sh.
  2. `devm snapshot create blob` — captures a stream archive via the
     hook pipeline. Verify the archive landed on the Mac side.
  3. Rewrite devm.yaml so `snapshots.blob` declares `path:
     /home/devm/blob-dir`, and strip the backup_blob / restore_blob
     hooks from devm.sh so validateSnapshots doesn't reject the new
     yaml as ambiguous (path + hook on the same entry).
  4. `devm snapshot restore blob` — expect non-zero exit with stderr
     naming the mode-drift refusal and pointing at `devm snapshot
     clear blob`.
  5. `mkdir -p /home/devm/blob-dir` so the forced restore has a tar
     target directory to extract into.
  6. `devm snapshot restore blob --force` — expect the drift refusal
     message NOT to appear in stderr. The forced restore bypasses the
     mode-drift check; the downstream tar step may still error on the
     raw stream bytes (not a tarball) and that's out of scope for
     this test — we pin the gate, not what happens after.

Pins:
  - archive-mode / yaml-mode drift refuses with a message that names
    the stream->path mismatch and the `devm snapshot clear` remediation;
  - `--force` suppresses that refusal (the drift message is absent
    from stderr), whatever the downstream tar step then does.
"""
from __future__ import annotations

import glob
import subprocess
from pathlib import Path

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


_DEVM_SH_STREAM = """#!/usr/bin/env bash
set -eo pipefail
backup_blob()  { echo HELLO; }
restore_blob() { cat > /tmp/blob.restored; }
"""

_DEVM_SH_PATH = """#!/usr/bin/env bash
set -eo pipefail
"""


def _snapshot_dir(workspace, entry: str) -> Path:
    """Mac-side archive directory for one snapshot entry under the
    devm-e2e identity's RuntimeDir."""
    runtime_dir = Path.home() / "Library" / "Application Support" / "devm-e2e"
    return runtime_dir / workspace.vm_name / "snapshots" / entry


@pytest.mark.slow
@pytest.mark.timeout(900)
def test_snapshot_drift_refusal(workspace, devm, sandbox_name):
    # Step 1: stream-mode yaml + backup_blob/restore_blob hooks.
    workspace.write_devmyaml(
        no_repo=True,
        snapshots={
            "blob": {},
        },
    )
    workspace.write_devm_sh(_DEVM_SH_STREAM)

    sandbox = TartSandbox(name=sandbox_name)

    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=600,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    # Step 2: capture a stream-mode archive.
    devm.snapshot("create", "blob", timeout=180)

    matches = glob.glob(str(_snapshot_dir(workspace, "blob") / "*.zst"))
    assert len(matches) >= 1, (
        f"expected at least one .zst archive for blob under "
        f"{_snapshot_dir(workspace, 'blob')}, got {matches}"
    )

    # Step 3: flip yaml to path mode on the same entry and strip the
    # hooks from devm.sh (path + hook on the same entry is rejected by
    # validateSnapshots as ambiguous).
    workspace.write_devmyaml(
        no_repo=True,
        snapshots={
            "blob": {"path": "/home/devm/blob-dir"},
        },
    )
    workspace.write_devm_sh(_DEVM_SH_PATH)

    # Step 4: restore refuses — archive is stream-mode but yaml now
    # declares path, and the error names `devm snapshot clear blob`.
    result = devm.snapshot("restore", "blob", timeout=180, check=False)
    assert result.returncode != 0, (
        f"expected mode-drift refusal, got exit 0:\n"
        f"stdout={result.stdout.decode()!r}\n"
        f"stderr={result.stderr.decode()!r}"
    )
    stderr = result.stderr.decode()
    assert "stream-mode" in stderr, (
        f"drift message missing 'stream-mode': {stderr!r}"
    )
    assert "yaml now declares path" in stderr, (
        f"drift message missing 'yaml now declares path': {stderr!r}"
    )
    assert "devm snapshot clear blob" in stderr, (
        f"drift message missing clear-remediation: {stderr!r}"
    )

    # Step 5: give the forced restore a target directory to extract
    # into. The path-mode TarRunner tar-extracts into this dir.
    r = sandbox.exec_shell("mkdir -p /home/devm/blob-dir")
    assert r.exit_code == 0, f"mkdir blob-dir failed: {r.stderr}"

    # Step 6: --force bypasses the drift refusal. The stream-mode
    # archive's raw bytes ("HELLO\n") are not a valid tarball, so the
    # downstream tar step may fail; we pin only that the drift gate
    # itself didn't fire — the refusal message must be absent from
    # stderr regardless of how tar reacts.
    forced = devm.snapshot("restore", "blob", "--force", timeout=180, check=False)
    forced_stderr = forced.stderr.decode()
    assert "archive is stream-mode" not in forced_stderr, (
        f"--force did not bypass drift refusal: {forced_stderr!r}"
    )
    assert "yaml now declares path" not in forced_stderr, (
        f"--force did not bypass drift refusal: {forced_stderr!r}"
    )
