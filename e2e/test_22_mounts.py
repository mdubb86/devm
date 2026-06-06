"""22: mounts — additional host paths mirror into the sandbox.

devm.yaml `mounts:` is a list of `HOST_PATH[:ro]` entries that get
passed to `sbx run` as positional workspaces. sbx mounts each at
the same path inside the VM (mirrored). The optional `:ro` suffix
makes the mount read-only.

This test:
  1. Creates a host tempdir with a marker file.
  2. Configures devm.yaml mounts: [<tempdir>:ro].
  3. Cold-starts via devm shell.
  4. Verifies the marker file is readable inside the sandbox at the
     SAME absolute host path.
  5. Verifies a write attempt fails (because :ro).
"""
import subprocess
import tempfile

import pytest

from helpers import Shell, sbx

pytestmark = pytest.mark.devm


@pytest.mark.timeout(90)
def test_mounts_mirrored_path_and_readonly(workspace, devm, sandbox_name, tmp_path):
    # Host-side fixture: a directory with a marker file.
    mount_src = tmp_path / "extra-mount"
    mount_src.mkdir()
    (mount_src / "MARKER").write_text("hello-from-host\n")

    workspace.write_devmyaml(
        install=["touch /tmp/install-marker"],
        mounts=[f"{mount_src}:ro"],
    )

    with Shell(devm, cwd=str(workspace.path)) as sh:
        sh.expect_prompt(timeout=90)

        # Marker file readable at the mirrored host path.
        sh.run_check(
            f"test -f {mount_src}/MARKER", expect_zero=True, timeout=15,
        )

        # Content matches.
        out = subprocess.run(
            ["sbx", "exec", sandbox_name, "cat", f"{mount_src}/MARKER"],
            capture_output=True, timeout=10, check=True,
        ).stdout.decode().strip()
        assert out == "hello-from-host", f"unexpected content: {out!r}"

        # Read-only enforcement: write attempt must fail.
        sh.run_check(
            f"touch {mount_src}/should-fail",
            expect_zero=False, timeout=15,
        )

        sh.exit(timeout=30)

    # Anchor-alive: explicitly stop after shell exit.
    devm.stop(yes=True)
    import time
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        if sbx.sandbox_state(sandbox_name) == "stopped":
            return
        time.sleep(0.5)
    pytest.fail(f"sandbox {sandbox_name} never reached 'stopped'")
