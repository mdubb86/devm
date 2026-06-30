"""68: install failure: file written to $WORKSPACE_DIR by root-in-VM is
readable and removable on the host without sudo.

Follow-on to test_67. Per test_67, a write to $WORKSPACE_DIR during a
failing install: persists on the host. But install: runs as the VM
user. This test pins what UID/perms that write produces on the host
filesystem, and whether the host-side process (running as the invoking
user, not root) can read AND delete it.

With virtio-fs on Tart / Apple Silicon, the UID mapping may differ from
pre-Tart era: observe and pin whatever Tart actually does.

If the host CANNOT delete the file without sudo, devm would need to
chown before the VM exits so devm can clean up failure records.
This test documents the observed behavior.

What this pins:
  - Host process can read a file written by the VM install: context via virtio-fs.
  - Host process can remove the file without sudo.
  - Observed UID/GID/mode are documented via print (not hard asserted,
    since the exact UID mapping is platform-specific).
"""
from __future__ import annotations

import os
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
def test_install_failure_workspace_file_readable_and_removable_on_host(
    workspace, devm
):
    workspace.write_devmyaml(
        install=[
            'touch "$WORKSPACE_DIR/probe.out"',
            'sh -c \'echo HELLO > "$WORKSPACE_DIR/probe.out"\'',
            "false",  # deliberate failure
        ],
    )

    p = subprocess.run(
        [devm.path, "shell", "--", "true"],
        capture_output=True, cwd=str(workspace.path), timeout=180,
    )
    assert p.returncode != 0, (
        f"devm shell should exit non-zero on failing install; got rc=0\n"
        f"stderr={p.stderr.decode()}"
    )
    vm = TartSandbox(name=workspace.vm_name)
    assert vm.state() == "absent", (
        f"failed install must not leave a VM behind; "
        f"VM is still in state {vm.state()!r}"
    )

    host_path = workspace.path / "probe.out"
    assert host_path.exists(), (
        f"probe.out not found on host; viability already covered by test_67. "
        f"devm output:\n{p.stdout.decode()}{p.stderr.decode()}"
    )

    # The contract: host process can read.
    content = host_path.read_text()
    assert content.rstrip() == "HELLO", (
        f"host file content mismatch: got {content!r}"
    )

    # Document observed ownership (not hard-asserted — the exact UID
    # mapping from root-in-VM to host depends on the virtio-fs config).
    st = os.stat(host_path)
    print(f"observed UID={st.st_uid} GID={st.st_gid} mode={oct(st.st_mode & 0o777)}")
    print(f"host process EUID={os.geteuid()}")

    # The contract: host process can remove without sudo.
    try:
        host_path.unlink()
    except PermissionError as e:
        pytest.fail(
            f"host cannot remove VM-written file without sudo. "
            f"Observed UID={st.st_uid}, host EUID={os.geteuid()}. "
            f"Error: {e}"
        )

    assert not host_path.exists(), "unlink appeared to succeed but file still present"
