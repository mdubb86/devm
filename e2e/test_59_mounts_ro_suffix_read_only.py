"""59: mounts: HOST_PATH:ro suffix makes the mount read-only inside the VM.

`mounts: ["HOST_PATH:ro"]` in devm.yaml mounts HOST_PATH as a
read-only virtio-fs share. Reads from inside the VM must succeed;
writes must fail with a non-zero exit code.

Ship 4 mechanism: the :ro suffix is forwarded to tart's --dir flag as
`--dir=name:HOST_PATH:ro`, producing a read-only virtio-fs share.

What this pins:
  - :ro mounts are readable from inside the VM.
  - :ro mounts reject writes (write command exits non-zero).

What it doesn't cover (tested elsewhere):
  - Writable (default) extra mounts -> test_58.
  - Workspace mount (always writable) -> test_56.
"""
from __future__ import annotations

import shutil
import subprocess
import tempfile

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_ro_suffix_makes_mount_read_only(workspace, devm, sandbox_name):
    ro_dir = tempfile.mkdtemp(prefix="devm-e2e-mount59-")
    try:
        # Plant a readable file in the ro mount directory.
        (workspace.path / "_keep").write_text("")  # ensure workspace non-empty
        source = f"{ro_dir}/SOURCE_59"
        with open(source, "w") as fh:
            fh.write("ro-source\n")

        workspace.write_devmyaml(
            mounts=[f"{ro_dir}:ro"],
        )

        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        tart_sandbox = TartSandbox(name=sandbox_name)
        assert tart_sandbox.state() == "running", (
            f"expected VM running; got {tart_sandbox.state()!r}"
        )

        # Reads must succeed.
        r_read = tart_sandbox.exec_shell(f"cat {source}")
        assert r_read.ok, (
            f"read from :ro mount should succeed: {r_read.stderr!r}"
        )
        assert r_read.stdout.strip() == "ro-source", (
            f"unexpected content: {r_read.stdout!r}"
        )

        # Writes must fail.
        r_write = tart_sandbox.exec_shell(
            f"echo trying > {ro_dir}/SHOULD_NOT_EXIST 2>&1; echo rc=$?"
        )
        assert r_write.ok, "the sh -c itself should run"
        out = r_write.stdout
        assert "rc=0" not in out, (
            f"write to :ro mount should fail with non-zero exit code; got: {out!r}"
        )
    finally:
        shutil.rmtree(ro_dir, ignore_errors=True)
