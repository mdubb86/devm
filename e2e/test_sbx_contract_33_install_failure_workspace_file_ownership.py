"""install failure: file written to $WORKSPACE_DIR by root-in-VM is readable
+ removable on the host without sudo.

Follow-on to c32. Per c32, a wrapper that writes to $WORKSPACE_DIR before
exiting non-zero gets its file persisted on the host. But install: runs
as root in the VM. The question this test answers: what UID/perms does
that root-in-VM-write produce on the host filesystem, and can devm's
host-side process (running as the user, not root) read AND delete it?

If the host CANNOT delete the file → wrapper must `chown $(stat -c %u
"$WORKSPACE_DIR") "$file"` (or similar) before exiting, so devm can
clean it up at next render without sudo. The contract tells us whether
the chown is needed.

The probe: same as c32 but inspects host-side ownership + write perms.

Devm dependency: internal design notes (refinement after R1) needs to know whether wrap-fg.sh must
chown its host-mirrored failure record. This contract resolves it.
"""
from __future__ import annotations

import os
import shutil
import subprocess
import tempfile

import pytest

from helpers import sbx
from helpers.contract import minimal_kit

pytestmark = pytest.mark.sbx_contract


@pytest.mark.timeout(120)
def test_install_failure_workspace_file_readable_and_removable_on_host(sandbox_name):
    ws = tempfile.mkdtemp(prefix="probe-c33-ws-")
    kit_dir = tempfile.mkdtemp(prefix="contract-kit-c33-")
    try:
        spec = minimal_kit(
            install=[
                'sh -c \'echo HELLO > "$WORKSPACE_DIR/probe.out"; exit 1\'',
            ],
        )
        with open(os.path.join(kit_dir, "spec.yaml"), "w") as f:
            f.write(spec)

        proc = subprocess.run(
            ["sbx", "run", "--kit", kit_dir, "--name", sandbox_name, "probe", ws],
            capture_output=True, timeout=90,
        )
        assert proc.returncode != 0, "sbx run should exit non-zero per contract_02"
        assert not sbx.sandbox_exists(sandbox_name)

        host_path = os.path.join(ws, "probe.out")
        assert os.path.exists(host_path), "viability already covered by c32"

        # The contract: host process can read.
        with open(host_path) as f:
            content = f.read()
        assert content.rstrip() == "HELLO"

        # Document observed ownership for the record (these print as part
        # of the test run; they're not assertions).
        st = os.stat(host_path)
        print(f"observed UID={st.st_uid} GID={st.st_gid} mode={oct(st.st_mode & 0o777)}")
        print(f"host process EUID={os.geteuid()}")

        # The contract: host process can remove without sudo.
        try:
            os.remove(host_path)
        except PermissionError as e:
            pytest.fail(
                f"host cannot remove root-in-VM-written file without sudo. "
                f"The wrapper MUST chown/chmod to a host-friendly UID "
                f"before exiting. Observed UID={st.st_uid}, host EUID={os.geteuid()}. "
                f"Error: {e}"
            )

        # Confirm the remove worked.
        assert not os.path.exists(host_path), "remove appeared to succeed but file still present"
    finally:
        subprocess.run(["sbx", "rm", "-f", sandbox_name],
                       capture_output=True, timeout=15)
        shutil.rmtree(kit_dir, ignore_errors=True)
        shutil.rmtree(ws, ignore_errors=True)
