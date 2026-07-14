"""99: a per-project `disk: <N>G` override grows the guest root filesystem."""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_disk_override_grows_root_fs(workspace, devm, sandbox_name):
    # Default VM disk is 32G; request 64G and expect the guest root fs
    # to come up well above the default after growpart/resize2fs.
    workspace.write_devmyaml(disk="64G")

    r = subprocess.run(
        [devm.path, "shell", "--", "true"],
        cwd=str(workspace.path),
        capture_output=True,
        timeout=300,
    )
    assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

    vm = TartSandbox(name=sandbox_name)
    assert vm.wait_running(timeout=120), "VM never reached running state"

    # `df -BG` reports the root-fs size in whole (binary) gigabytes, size
    # column of the `/` row, digits only. A 64G tart disk comes back as
    # ~59G after EFI/GPT partitioning + ext4 overhead and df's GiB
    # rounding; a 32G default lands near ~29-30G. A threshold of 50 sits
    # cleanly in that gap — it passes for the 64G override and would fail
    # for a default-sized disk.
    r = vm.exec_shell("df -BG --output=size / | tail -1 | tr -dc 0-9")
    assert r.ok, f"df failed: {r.stderr!r}"
    size_gb = int(r.stdout.strip())
    assert size_gb >= 50, (
        f"root fs is {size_gb}G; expected ~59G for a 64G override (well above "
        f"the ~29G a 32G default yields) — disk override did not grow the guest filesystem"
    )
