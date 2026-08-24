"""149: devm cp bidirectional file copy between Mac and the sandbox.

Pins the three code paths devm cp routes through:

  A. Upload via mount transport: writing into a workspace-mounted path
     is a Mac-side io.Copy through virtio-fs (no `tart exec` round-trip).
  B. Upload via pipe + sudo retry: writing to /etc/foo.conf fails as
     the devm user, retries via `sudo tee`, succeeds. Also proves the
     "wrote via sudo" log line lands on stderr.
  C. Download via pipe: reading a root-owned file rounds-trips content
     back to a Mac tempfile.

Only exercises `devm cp` on a project whose VM is already up — no VM
lifecycle mutation — so runs under the `devm` marker (no sudo/Touch
ID required).
"""
from __future__ import annotations

import subprocess
from pathlib import Path

import pytest

pytestmark = pytest.mark.devm


@pytest.mark.slow
@pytest.mark.timeout(600)
def test_devm_cp_upload_mount_and_pipe_and_download(devm, workspace, sandbox_name, devm_installed, tmp_path):
    workspace.write_devmyaml(config_lock=False)

    try:
        # 1. Cold-start.
        r = subprocess.run(
            [devm.path, "shell", "--", "true"],
            cwd=str(workspace.path), capture_output=True, timeout=300,
        )
        assert r.returncode == 0, (
            f"cold-start failed:\n"
            f"stdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )

        # ============================================================
        # A. Upload via MOUNT transport — target path is inside the
        #    workspace. devm cp resolves the guest path to the primary
        #    volume's Mac-side storage internally (mountPassthrough in
        #    cmd/devm/cp.go) and writes there directly; the guest sees
        #    it immediately via the live virtiofs share. The Mac-side
        #    write location is an implementation detail this test
        #    doesn't need to know — it only asserts guest-visible
        #    content via `devm exec`.
        # ============================================================
        mount_src = tmp_path / "hello.txt"
        mount_src.write_text("hello via mount\n")
        mount_dst_guest = f"{workspace.path}/mount-copy.txt"

        r = subprocess.run(
            [devm.path, "cp", str(mount_src), f":{mount_dst_guest}"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, (
            f"mount upload failed:\n"
            f"stdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )
        assert b"(mount)" in r.stderr, (
            f"expected 'mount' transport log line, got stderr={r.stderr.decode()!r}"
        )
        # Guest sees it at the same absolute path (virtio-fs).
        gr = subprocess.run(
            [devm.path, "exec", "cat", mount_dst_guest],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert gr.returncode == 0, f"guest cat failed:\n{gr.stderr.decode()!r}"
        assert gr.stdout == b"hello via mount\n"

        # ============================================================
        # B. Upload via PIPE + sudo retry — /etc/devm-cp-probe.conf is
        #    root-owned; the devm user's initial `tee` errors EACCES,
        #    devm cp retries with `sudo tee`, succeeds. Log line has
        #    "sudo" annotation.
        # ============================================================
        pipe_src = tmp_path / "config.conf"
        pipe_src.write_text("key=value\n")
        pipe_dst_guest = "/etc/devm-cp-probe.conf"

        r = subprocess.run(
            [devm.path, "cp", str(pipe_src), f":{pipe_dst_guest}"],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, (
            f"pipe+sudo upload failed:\n"
            f"stdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )
        assert b"(pipe, sudo)" in r.stderr, (
            f"expected 'pipe, sudo' transport log line (proves the retry "
            f"branch fired), got stderr={r.stderr.decode()!r}"
        )
        # Guest confirms content.
        gr = subprocess.run(
            [devm.path, "exec", "cat", pipe_dst_guest],
            cwd=str(workspace.path), capture_output=True, timeout=15,
        )
        assert gr.returncode == 0
        assert gr.stdout == b"key=value\n"

        # ============================================================
        # C. Download via PIPE — read the file we just wrote back out
        #    to a Mac tempfile. Roundtrip proves guest→host works.
        # ============================================================
        download_dst = tmp_path / "roundtrip.conf"
        r = subprocess.run(
            [devm.path, "cp", f":{pipe_dst_guest}", str(download_dst)],
            cwd=str(workspace.path), capture_output=True, timeout=30,
        )
        assert r.returncode == 0, (
            f"pipe download failed:\n"
            f"stdout={r.stdout.decode()!r}\n"
            f"stderr={r.stderr.decode()!r}"
        )
        assert download_dst.read_bytes() == b"key=value\n", (
            f"roundtrip content mismatch: got {download_dst.read_bytes()!r}"
        )
    finally:
        # Clean up guest-side probe file (root-owned, need sudo). Best
        # effort — a stale /etc/devm-cp-probe.conf across runs would
        # pass the test anyway (the write is idempotent) but leaves
        # the guest cluttered.
        subprocess.run(
            [devm.path, "exec", "sudo", "rm", "-f", "/etc/devm-cp-probe.conf"],
            cwd=str(workspace.path), capture_output=True, timeout=10,
        )
        subprocess.run(
            [devm.path, "teardown", "--yes"],
            cwd=str(workspace.path), capture_output=True, timeout=60,
        )
