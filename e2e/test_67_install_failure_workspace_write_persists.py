"""67: install failure: file written to $WORKSPACE_DIR by a failing install
step persists on the host filesystem after devm teardown.

Pins the virtio-fs invariant: files written to $WORKSPACE_DIR during
install: persist on the host even after the VM is torn down. Because
$WORKSPACE_DIR is the same absolute path as the host workspace
(virtio-fs mirrored paths), writes inside the VM land in the shared
directory and survive VM teardown.

The probe:
  - install step 1: write a marker to $WORKSPACE_DIR/install-wrote.txt
  - install step 2: exit 1 (deliberate failure)

After `devm shell` exits non-zero, we verify that install-wrote.txt
still exists on the host at workspace.path/install-wrote.txt.

Devm dependency: virtio-fs writes to $WORKSPACE_DIR survive VM teardown.
This test locks in the foundational property.
"""
from __future__ import annotations

import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.xfail(
    strict=False,
    reason=(
        "devm bug B: orchestrator/shell.go RunShell returns provision error without "
        "VM teardown, leaving a zombie VM. Remove xfail when bug B lands."
    ),
)
@pytest.mark.timeout(180)
def test_install_failure_workspace_write_persists_on_host(workspace, devm):
    workspace.write_devmyaml(
        install=[
            'touch "$WORKSPACE_DIR/install-wrote.txt"',
            "false",  # deliberate failure
        ],
    )

    # Cold-start; expect failure.
    p = subprocess.run(
        [devm.path, "shell", "--", "true"],
        capture_output=True, cwd=str(workspace.path), timeout=180,
    )
    assert p.returncode != 0, (
        f"devm shell should exit non-zero on failing install; got rc=0\n"
        f"stderr={p.stderr.decode()}"
    )

    # VM should be gone (loud failure per test_51).
    vm = TartSandbox(name=workspace.vm_name)
    assert vm.state() == "absent", (
        f"failed install must not leave a VM behind; "
        f"VM is still in state {vm.state()!r}"
    )

    # The viability pin: the workspace write from step 1 must persist
    # on the host even though the VM was torn down.
    host_path = workspace.path / "install-wrote.txt"
    assert host_path.exists(), (
        f"VM-side write to $WORKSPACE_DIR did NOT persist on host after "
        f"install failure + VM teardown. The virtio-fs write-and-survive "
        f"invariant is broken. devm output:\n"
        f"{p.stdout.decode()}{p.stderr.decode()}"
    )
