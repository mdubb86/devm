"""67+68: install failure — $WORKSPACE writes persist, are readable, and
are removable on the host after VM teardown.

Merges two tests using the identical `install=[touch, ..., false]` shape
against a failing install: — test_68 is a strict superset of test_67 (same
"rc != 0 + VM absent + host file exists" checks, plus content verification
and ownership/removability), so one boot proves both.

Pins the virtio-fs invariant: files written to $WORKSPACE during a failing
install: step persist on the host even after the VM is torn down (Bug B's
teardown-on-fail). Because $WORKSPACE is the same absolute path as the host
workspace (virtio-fs mirrored paths), writes inside the VM land in the
shared directory and survive VM teardown.

The probe:
  - install step 1: write a marker to $WORKSPACE/install-wrote.txt
  - install step 2: overwrite it with known content ("HELLO")
  - install step 3: sync (forces the virtiofs write-back cache to flush to
    the host before teardown fires — otherwise the write can be lost when
    tart delete kills the guest before its dirty pages reach the shared
    workspace)
  - install step 4: exit 1 (deliberate failure)

What this pins:
  - devm shell exits non-zero on failing install: (was test_67 + test_68).
  - Failed install: leaves no VM behind (loud failure per test_51) (was
    test_67 + test_68).
  - The workspace write from install: persists on the host even though the
    VM was torn down (was test_67).
  - Host process can read the file's content via virtio-fs (was test_68).
  - Observed UID/GID/mode are documented via print (not hard asserted,
    since the exact UID mapping is platform-specific) (was test_68).
  - Host process can remove the file without sudo (was test_68).
"""
from __future__ import annotations

import os
import subprocess

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_install_failure_workspace_write_persists_and_is_removable(workspace, devm):
    workspace.write_devmyaml(
        install=[
            'touch "$WORKSPACE/install-wrote.txt"',
            'sh -c \'echo HELLO > "$WORKSPACE/install-wrote.txt"\'',
            # sync forces the virtiofs write-back cache to flush to the
            # host before Bug B's teardown-on-fail fires — otherwise the
            # write can be lost when tart delete kills the guest before
            # its dirty pages reach the shared workspace.
            "sync",
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
    current = vm.state()
    assert current == "absent", (
        f"failed install must not leave a VM behind; "
        f"VM is still in state {current!r}"
    )

    # The viability pin: the workspace write from install: must persist
    # on the host even though the VM was torn down.
    host_path = workspace.path / "install-wrote.txt"
    assert host_path.exists(), (
        f"VM-side write to $WORKSPACE did NOT persist on host after "
        f"install failure + VM teardown. The virtio-fs write-and-survive "
        f"invariant is broken. devm output:\n"
        f"{p.stdout.decode()}{p.stderr.decode()}"
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
