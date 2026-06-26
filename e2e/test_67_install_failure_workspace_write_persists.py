"""67: install failure: file written to $WORKSPACE_DIR by a failing install
step persists on the host filesystem after devm teardown.

Smoke-tests the viability of devm's "wrapper mirrors failure record to
the workspace mount" approach for surviving install failures. When
install: fails, devm tears down the VM — the VM's tmpfs is gone. But
files written to $WORKSPACE_DIR ARE the workspace (virtio-fs mirrored),
which lives on the host.

The probe:
  - install step 1: write a marker to $WORKSPACE_DIR/install-wrote.txt
  - install step 2: exit 1 (deliberate failure)

After `devm shell` exits non-zero, we verify that install-wrote.txt
still exists on the host at workspace.path/install-wrote.txt.

Devm dependency: wrap-fg.sh mirrors failure records to $WORKSPACE_DIR
before exiting with the user rc, so devm can read them post-teardown
on the host. This test locks in the foundational property that
virtio-fs writes survive VM teardown.
"""
from __future__ import annotations

import subprocess

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_install_failure_workspace_write_persists_on_host(workspace, devm, tart_sandbox):
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
    assert tart_sandbox.state() == "absent", (
        f"failed install must not leave a VM behind; "
        f"VM is still in state {tart_sandbox.state()!r}"
    )

    # The viability pin: the workspace write from step 1 must persist
    # on the host even though the VM was torn down.
    host_path = workspace.path / "install-wrote.txt"
    assert host_path.exists(), (
        f"VM-side write to $WORKSPACE_DIR did NOT persist on host after "
        f"install failure + VM teardown. The wrapper-writes-failure-record "
        f"approach is not viable. devm output:\n"
        f"{p.stdout.decode()}{p.stderr.decode()}"
    )
