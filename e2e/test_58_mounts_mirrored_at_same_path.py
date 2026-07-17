"""58: extra mounts: entries — mirrored path, :ro enforcement, persistence.

`mounts: [HOST_PATH]` in devm.yaml mirrors HOST_PATH at the same
absolute path inside the VM (virtio-fs share). `mounts: [HOST_PATH:ro]`
does the same but read-only. Data written to a mount survives a
`devm stop` + `devm shell` restart cycle (host-backed virtio-fs share,
not guest-local storage).

Ship 4 mechanism: tart run --dir=name:HOST_PATH[:ro] mounts the host
dir as a virtio-fs share. The devm schema's `mounts:` list entries are
rendered as additional --dir flags alongside the workspace mount.

Single cold-start VM covers all of it — one `mounts:` entry writable,
one `:ro`.

What this pins:
  - A plain mounts: entry with an absolute host path produces a
    mountpoint inside the VM at that exact path (mountpoint -q).
  - Files written to the host path are visible inside the VM at the
    identical path, content byte-for-byte.
  - A `:ro`-suffixed mounts: entry is mirrored the same way, is
    readable, but rejects writes from inside the VM.
  - Data written to a plain mount from inside the VM survives a
    `devm stop --yes` + `devm shell` restart (virtio-fs share persists
    across VM reboot, not just guest-local write).

What it doesn't cover (tested elsewhere):
  - Workspace mount (primary) -> test_56.
  - Live mounts: add/remove on a running sandbox: not pinned — a
    mounts diff forces BucketTeardownShell (recreate), not a live
    apply (see internal/reconcile/diff_test.go:373), so this is
    generic recreate-path behavior, not mounts-specific.
"""
from __future__ import annotations

import shutil
import subprocess
import tempfile
import time

import pytest

from helpers.tart import TartSandbox

pytestmark = pytest.mark.devm


@pytest.mark.timeout(300)
def test_mounts_mirrored_readonly_and_persist(workspace, devm, sandbox_name):
    rw_dir = tempfile.mkdtemp(prefix="devm-e2e-mount58-rw-")
    ro_dir = tempfile.mkdtemp(prefix="devm-e2e-mount58-ro-")
    try:
        rw_marker = f"{rw_dir}/EXTRA_MARK_58"
        with open(rw_marker, "w") as fh:
            fh.write("extra-ok\n")

        ro_source = f"{ro_dir}/SOURCE_59"
        with open(ro_source, "w") as fh:
            fh.write("ro-source\n")

        workspace.write_devmyaml(
            mounts=[rw_dir, f"{ro_dir}:ro"],
        )

        # Owns cold-start: extra mounts are baked into `tart run --dir`
        # args, so the yaml must be in place before the first devm shell.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, f"cold-start failed:\n{r.stderr.decode()}"

        tart_sandbox = TartSandbox(name=sandbox_name)
        current = tart_sandbox.state()
        assert current == "running", f"expected VM running; got {current!r}"

        # ---- Plain mount: mirrored path + content + real mountpoint. ----
        r = tart_sandbox.exec_shell(f"cat {rw_marker}")
        assert r.ok, f"rw mount not visible at {rw_marker}: {r.stderr!r}"
        assert r.stdout.strip() == "extra-ok", (
            f"unexpected rw marker content: {r.stdout!r}"
        )

        r = tart_sandbox.exec("mountpoint", "-q", rw_dir)
        assert r.exit_code == 0, (
            f"declared mount {rw_dir!r} is not a mountpoint inside the VM "
            f"(exit_code={r.exit_code}, stderr={r.stderr!r})"
        )

        # ---- :ro mount: mirrored path + content readable + write fails. ----
        r_read = tart_sandbox.exec_shell(f"cat {ro_source}")
        assert r_read.ok, (
            f"read from :ro mount should succeed: {r_read.stderr!r}"
        )
        assert r_read.stdout.strip() == "ro-source", (
            f"unexpected ro content: {r_read.stdout!r}"
        )

        r_write = tart_sandbox.exec_shell(
            f"echo trying > {ro_dir}/SHOULD_NOT_EXIST 2>&1; echo rc=$?"
        )
        assert r_write.ok, "the sh -c itself should run"
        assert "rc=0" not in r_write.stdout, (
            f"write to :ro mount should fail with non-zero exit code; "
            f"got: {r_write.stdout!r}"
        )

        # ---- Persistence: write to the rw mount, stop, restart, re-read. ----
        probe_path = f"{rw_dir}/probe-vol70"
        r = tart_sandbox.exec_shell(f"echo persist > {probe_path}")
        assert r.exit_code == 0, (
            f"failed to write probe file inside VM: {r.stderr!r}"
        )
        r = tart_sandbox.exec_shell(f"cat {probe_path}")
        assert r.exit_code == 0 and r.stdout.strip() == "persist", (
            f"probe file not immediately readable: exit={r.exit_code} out={r.stdout!r}"
        )

        devm.stop(yes=True, timeout=30)

        stopped_state = tart_sandbox.wait_state("stopped", timeout=15)
        assert stopped_state == "stopped", (
            f"VM should be stopped after devm stop; got {stopped_state!r}"
        )

        subprocess.run(
            [devm.path, "shell", "--", "true"],
            capture_output=True, cwd=str(workspace.path), timeout=180,
        )

        # Bumped from 30s to 90s — cold-start can queue behind other
        # tests' /vm/start under the parallel phase; 30s was tight even
        # standalone.
        deadline = time.monotonic() + 90
        final_state = "unknown"
        while time.monotonic() < deadline:
            final_state = tart_sandbox.state()
            if final_state == "running":
                break
            time.sleep(0.5)
        assert final_state == "running", (
            f"VM should be running after restart; got {final_state!r}"
        )

        r = tart_sandbox.exec_shell(f"cat {probe_path}")
        assert r.exit_code == 0, (
            f"probe file missing after stop/restart — mount data did not "
            f"persist: exit={r.exit_code} stderr={r.stderr!r}"
        )
        assert r.stdout.strip() == "persist", (
            f"probe file content corrupted after stop/restart: {r.stdout!r}"
        )
    finally:
        shutil.rmtree(rw_dir, ignore_errors=True)
        shutil.rmtree(ro_dir, ignore_errors=True)
