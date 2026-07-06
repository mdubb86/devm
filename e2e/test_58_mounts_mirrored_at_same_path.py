"""58: extra mounts: entries visible at the same absolute path inside VM.

`mounts: [HOST_PATH]` in devm.yaml mirrors HOST_PATH at the same
absolute path inside the VM (virtio-fs share). A file written to
HOST_PATH on the host before cold-start must be readable inside at
exactly HOST_PATH.

Ship 4 mechanism: tart run --dir=name:HOST_PATH mounts the host dir
as a virtio-fs share. The devm schema's `mounts:` list entries are
rendered as additional --dir flags alongside the workspace mount.

What this pins:
  - A mounts: entry with an absolute host path produces a mount inside
    the VM at that exact path.
  - Files written to the host path are visible inside the VM.

What it doesn't cover (tested elsewhere):
  - Read-only :ro suffix -> test_59.
  - Workspace mount (primary) -> test_56.
"""
from __future__ import annotations

import shutil
import subprocess
import tempfile

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(180)
def test_extra_mount_mirrored_at_same_path(workspace, devm, sandbox_name):
    extra = tempfile.mkdtemp(prefix="devm-e2e-mount58-")
    try:
        # Plant a marker file on the host side of the extra mount.
        (workspace.path / "_keep").write_text("")  # ensure workspace non-empty
        marker = f"{extra}/EXTRA_MARK_58"
        with open(marker, "w") as fh:
            fh.write("extra-ok\n")

        workspace.write_devmyaml(
            mounts=[extra],
        )

        # Owns cold-start: extra mounts are baked into `tart run --dir` args,
        # so the yaml must be in place before the first devm shell.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        tart_sandbox = TartSandbox(name=sandbox_name)
        current = tart_sandbox.state()
        assert current == "running", f"expected VM running; got {current!r}"

        # Extra mount must be visible at the exact same host path inside the VM.
        r = tart_sandbox.exec_shell(f"cat {marker}")
        assert r.ok, (
            f"extra mount not visible at {marker}: {r.stderr!r}"
        )
        assert r.stdout.strip() == "extra-ok", (
            f"unexpected marker content: {r.stdout!r}"
        )
    finally:
        shutil.rmtree(extra, ignore_errors=True)
