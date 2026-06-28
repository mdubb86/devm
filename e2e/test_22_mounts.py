"""22: devm.yaml mounts: entries mirror host paths into the sandbox at the same absolute path, honoring the :ro suffix.

User configures `mounts: [<host_path>:ro]` and cold-starts via
`devm shell`. The host path appears inside the sandbox at the same
absolute path, file contents are readable, and write attempts fail
because the entry was declared read-only.

What this pins:
  - Marker file under the mounted host dir is readable inside the
    sandbox at the identical absolute path.
  - File content read via tart_sandbox.exec matches the host-written
    content byte-for-byte.
  - A `touch` inside the read-only mount returns non-zero.

What it doesn't cover (tested elsewhere):
  - virtio-fs mirror + :ro semantics in isolation:
    test_58_mounts_mirrored_at_same_path and
    test_59_mounts_ro_suffix_read_only.
  - Live mounts: change (add/remove a mounts entry on a running
    sandbox): not yet pinned.
"""
import pytest

from helpers import Shell, stop_and_wait_stopped

pytestmark = pytest.mark.devm


@pytest.mark.timeout(90)
def test_mounts_mirrored_path_and_readonly(workspace, devm, tart_sandbox, sandbox_name, tmp_path):
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

        # Content matches — verify via direct VM exec (no wrapper).
        result = tart_sandbox.exec("cat", f"{mount_src}/MARKER")
        assert result.ok, f"cat failed: {result.stderr!r}"
        assert result.stdout.strip() == "hello-from-host", (
            f"unexpected content: {result.stdout!r}"
        )

        # Read-only enforcement: write attempt must fail.
        sh.run_check(
            f"touch {mount_src}/should-fail",
            expect_zero=False, timeout=15,
        )

        sh.exit(timeout=30)

    # Anchor-alive: explicitly stop after shell exit.
    stop_and_wait_stopped(devm, sandbox_name)
