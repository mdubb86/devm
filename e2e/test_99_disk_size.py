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

    # `df -BG` prints the root fs size in whole gigabytes; the size column
    # of the `/` row, digits only.
    r = vm.exec_shell("df -BG --output=size / | tail -1 | tr -dc 0-9")
    assert r.ok, f"df failed: {r.stderr!r}"
    size_gb = int(r.stdout.strip())
    assert size_gb >= 60, (
        f"root fs is {size_gb}G; expected ~64G (well above the 32G default) — "
        f"disk override did not grow the guest filesystem"
    )
