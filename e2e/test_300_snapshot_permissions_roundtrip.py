"""300: snapshot create/restore round-trip preserves uid/gid/mode for
both user-owned and root-owned data.

Flow:
  1. Declare two snapshot entries — `appstate` (user-owned at
     /home/devm/.appstate) and `buildkit` (root-owned at
     /var/lib/testdata). Pair each with backup/restore hooks in
     devm.sh.
  2. Cold-start, seed each directory with a marker file at known
     uid/gid/mode.
  3. `devm snapshot create` both entries; assert archives on the Mac
     side under `<RuntimeDir>/<projectID>/snapshots/<entryName>/`.
  4. `devm teardown` — markers are gone.
  5. Cold-start again — fresh VM, confirm markers absent.
  6. `devm snapshot restore` both entries; assert marker content and
     permissions (uid:gid mode) survived round-trip.

Pins: root-owned data restores as root:root (tar must be re-run under
sudo in the restore hook, not just owned by devm post-extract), and
user-owned data stays owned by the devm user.
"""
from __future__ import annotations

import glob
import subprocess
from pathlib import Path

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


_DEVM_SH = """#!/usr/bin/env bash
set -eo pipefail
backup_appstate()   { tar c -C /home/devm/.appstate .; }
restore_appstate()  { tar x -C /home/devm/.appstate; }
backup_buildkit()   { sudo tar c -C /var/lib/testdata .; }
restore_buildkit()  { sudo tar x -C /var/lib/testdata; }
"""


def _snapshot_dir(workspace, entry: str) -> Path:
    """Mac-side archive directory for one snapshot entry under the
    devm-e2e identity's RuntimeDir."""
    runtime_dir = Path.home() / "Library" / "Application Support" / "devm-e2e"
    return runtime_dir / workspace.vm_name / "snapshots" / entry


@pytest.mark.slow
@pytest.mark.timeout(1800)
def test_snapshot_permissions_roundtrip(workspace, devm, sandbox_name):
    workspace.write_devmyaml(
        no_repo=True,
        snapshots={
            "appstate": {},
            "buildkit": {},
        },
    )
    workspace.write_devm_sh(_DEVM_SH)

    sandbox = TartSandbox(name=sandbox_name)

    # Cold-start — first-run auto-approves.
    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=600,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    # Seed the two directories inside the guest with distinct owners.
    # appstate: devm:devm, marker mode 0640.
    r = sandbox.exec_shell(
        "install -d -m 0750 -o devm -g devm /home/devm/.appstate && "
        "echo appstate-marker > /home/devm/.appstate/marker && "
        "chmod 0640 /home/devm/.appstate/marker"
    )
    assert r.exit_code == 0, f"seed appstate failed: {r.stderr}"

    # buildkit: root:root, marker mode 0600.
    r = sandbox.exec_shell(
        "sudo install -d -m 0700 -o root -g root /var/lib/testdata && "
        "echo buildkit-marker | sudo tee /var/lib/testdata/marker > /dev/null && "
        "sudo chmod 0600 /var/lib/testdata/marker && "
        "sudo chown root:root /var/lib/testdata/marker"
    )
    assert r.exit_code == 0, f"seed buildkit failed: {r.stderr}"

    # Capture both snapshots.
    devm.snapshot("create", "appstate", timeout=180)
    devm.snapshot("create", "buildkit", timeout=180)

    # Assert one archive per entry landed on the Mac side.
    for entry in ("appstate", "buildkit"):
        matches = glob.glob(str(_snapshot_dir(workspace, entry) / "*.zst"))
        assert len(matches) >= 1, (
            f"expected at least one .zst archive for {entry!r} under "
            f"{_snapshot_dir(workspace, entry)}, got {matches}"
        )

    # Teardown — destroys the VM and wipes guest state.
    devm.teardown(yes=True, skip_snapshot=True, timeout=120)

    # Fresh cold-start — same project name/VM name, new disk.
    r = subprocess.run(
        [devm.path, "start"],
        cwd=str(workspace.path), capture_output=True, timeout=600,
    )
    assert r.returncode == 0, f"second cold-start failed:\n{r.stderr.decode()}"

    # Markers must be absent (teardown wiped disk, fresh boot).
    r = sandbox.exec("test", "-f", "/home/devm/.appstate/marker")
    assert r.exit_code != 0, "appstate marker survived teardown + fresh boot"
    r = sandbox.exec_shell("sudo test -f /var/lib/testdata/marker")
    assert r.exit_code != 0, "buildkit marker survived teardown + fresh boot"

    # The restore hooks tar-extract into existing directories; create
    # them with the right ownership first (the backup captured only
    # the contents of each directory, not the directory itself).
    r = sandbox.exec_shell(
        "install -d -m 0750 -o devm -g devm /home/devm/.appstate && "
        "sudo install -d -m 0700 -o root -g root /var/lib/testdata"
    )
    assert r.exit_code == 0, f"recreate target dirs failed: {r.stderr}"

    # Restore both entries.
    devm.snapshot("restore", "appstate", timeout=180)
    devm.snapshot("restore", "buildkit", timeout=180)

    # appstate marker: content + uid:gid + mode.
    r = sandbox.exec_shell("cat /home/devm/.appstate/marker")
    assert r.exit_code == 0 and r.stdout.strip() == "appstate-marker", (
        f"appstate marker missing or corrupt after restore: {r.stdout!r} / {r.stderr!r}"
    )
    r = sandbox.exec_shell(
        "stat -c '%u:%g %a' /home/devm/.appstate/marker"
    )
    assert r.exit_code == 0, f"stat appstate marker failed: {r.stderr}"
    # devm user default uid/gid is 1000.
    assert r.stdout.strip() == "1000:1000 640", (
        f"appstate marker perms wrong after restore: got {r.stdout!r}"
    )
    # Parent directory uid/gid + mode: the `tar c -C <dir> .` capture
    # stores a `./` entry, so `tar x -C <dir>` resets the directory's
    # perms on restore — pin them so a bug that drops the dir back to
    # 0755 root:root surfaces.
    r = sandbox.exec_shell(
        "stat -c '%u:%g %a' /home/devm/.appstate"
    )
    assert r.exit_code == 0, f"stat appstate parent dir failed: {r.stderr}"
    assert r.stdout.strip() == "1000:1000 750", (
        f"appstate parent dir perms wrong after restore: got {r.stdout!r}"
    )

    # buildkit marker: content + uid:gid + mode (root-owned).
    r = sandbox.exec_shell("sudo cat /var/lib/testdata/marker")
    assert r.exit_code == 0 and r.stdout.strip() == "buildkit-marker", (
        f"buildkit marker missing or corrupt after restore: {r.stdout!r} / {r.stderr!r}"
    )
    r = sandbox.exec_shell(
        "sudo stat -c '%u:%g %a' /var/lib/testdata/marker"
    )
    assert r.exit_code == 0, f"stat buildkit marker failed: {r.stderr}"
    assert r.stdout.strip() == "0:0 600", (
        f"buildkit marker perms wrong after restore: got {r.stdout!r}"
    )
    # Parent directory uid/gid + mode: 0700 root:root isn't readable
    # to the devm user, so `stat` needs sudo too.
    r = sandbox.exec_shell(
        "sudo stat -c '%u:%g %a' /var/lib/testdata"
    )
    assert r.exit_code == 0, f"stat buildkit parent dir failed: {r.stderr}"
    assert r.stdout.strip() == "0:0 700", (
        f"buildkit parent dir perms wrong after restore: got {r.stdout!r}"
    )
